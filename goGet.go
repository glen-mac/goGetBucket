package main

import (
	"bufio"
	"flag"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"os"
	"strings"
	"sync"
	"time"
)

/* state of the system */
type State struct {
	InputFileName  string   /* the input wordlist */
	OutputFileName string   /* the output file name */
	OutputFile     *os.File /* the output file handler */
	Threads        int      /* the number of threads to use */
	MutateFileName string   /* the mutation file name */
	DomainName     string   /* the name of the domain */
	Buckets        int      /* the number of buckets read */
}

/* state of bucket test return */
type Result struct {
	Name      string /* bucket name */
	Region    string /* the bucket region */
	Status    bool   /* was there an error? */
	Listable  bool   /* bucket listable status */
	Writeable bool   /* bucket writeable status */
}

/* list of all s3 regions */
var regionList = []string{"us-east-2", "us-east-1", "us-west-1", "us-west-2",
	"ca-central-1", "ap-south-1", "ap-northeast-2", "ap-southeast-1",
	"ap-southeast-2", "ap-northeast-1", "eu-central-1", "eu-west-1",
	"eu-west-2", "sa-east-1"}

/* define separators for mutation */
var separators = []string{".", "-", "_", ""}

/*  checkBucket
check if a bucket with a certain name exists
*/
func checkBucket(s *State, bucket string, resultChan chan<- Result, region string) {

	/* create the s3 session object */
	s3svc := s3.New(session.New(), aws.NewConfig().WithRegion(region))

	/* check if the bucket exists at all */
	lor := &s3.ListObjectsInput{
		Bucket:  aws.String(bucket),
		MaxKeys: aws.Int64(0),
	}
	_, err := s3svc.ListObjects(lor)

	/* init the result struct */
	r := Result{
		Name:      bucket,
		Region:    region,
		Status:    (err == nil),
		Listable:  true,
		Writeable: false,
	}

	/* handle the return code if we couldn't list the bucket */
	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok {
			switch awsErr.Code() {
			case "NoSuchBucket":
				return
			case "BucketRegionError":
				foundRegion := discoverRegion(bucket)
				if foundRegion != "no_region_found" {
					checkBucket(s, bucket, resultChan, foundRegion)
				} else {
					fmt.Printf("* {checkBucket} got 'no_region_found' on bucket %s", bucket)
				}
				/* case "RequestLimitExceeded":
				   fmt.Println("rate limit exceeded")*/
			case "AccessDenied":
				r.Listable = false
				resultChan <- r
			default:
				fmt.Printf("[%s]\tbucket: %s\tregion: %s\n", awsErr.Code(), bucket, region)
			}
		}
	} else {
		resultChan <- r
	}
}

/* discoverRegion
the point of this function is to find the region in which this bucket belongs
*/
func discoverRegion(bucket string) string {

	/* create new session object with default region */
	s3svc := s3.New(session.New(), aws.NewConfig().WithRegion("us-west-2"))

	/* check if we have permissions to use the 'get bucket region' endpoint */
	ctx := &s3.GetBucketLocationInput{
		Bucket: aws.String(bucket),
	}
	response, err := s3svc.GetBucketLocation(ctx)

	/* no permissions to get region, so bruteforce it lol */
	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok && awsErr.Code() == "AccessDenied" {
			lor := &s3.ListObjectsInput{
				Bucket:  aws.String(bucket),
				MaxKeys: aws.Int64(0),
			}
			for _, region := range regionList {
				//fmt.Printf("bruteforce for bucket %s with region %s\n", bucket, region)
				s3svc = s3.New(session.New(), aws.NewConfig().WithRegion(region))
				_, err := s3svc.ListObjects(lor)
				if err == nil || err.(awserr.Error).Code() == "AccessDenied" {
					return region
				}
			}
			return "no_region_found"
		} else {
			fmt.Printf("* {discoverRegion} [%s] (weird error) bucket: '%s'\n", awsErr.Code(), bucket)
			return "no_region_found"
		}
	}

	/* otherwise we got a return value from the 'get bucket region' call */
	var region string
	if response.LocationConstraint == nil {
		// US Classic does not return a region
		region = "us-east-1"
	} else {
		region = *response.LocationConstraint
		// Another special case: "EU" can mean eu-west-1
		if region == "EU" {
			region = "eu-west-1"
		}
	}
	fmt.Printf("* {discoverRegion} 'get bucket region' call successful with bucket: '%s' and region: '%s'\n", bucket, region)
	return region
}

/*  parseArgs
parse the command line arguments and store the state to use throughout
the program usage
*/
func parseArgs() *State {
	s := new(State)
	flag.IntVar(&s.Threads, "t", 100, "Number of concurrent threads")
	flag.StringVar(&s.InputFileName, "i", "", "Path to input wordlist to enumerate")
	flag.StringVar(&s.OutputFileName, "o", "", "Path to output file to store log")
	flag.StringVar(&s.MutateFileName, "m", "", "Path to mutation wordlist (requires domain flag)")
	flag.StringVar(&s.DomainName, "d", "", "Supplied domain name (used with mutation flag)")
	flag.Parse()
	return s
}

/* printResults
parse a result object and print it in a desired manner
*/
func printResults(s *State, r *Result) {
	fmt.Printf("Bucket: %s\tregion: %s\n\thas L/W: %t/%t\n", r.Name, r.Region, r.Listable, r.Writeable)
	if s.OutputFile != nil {
		s.OutputFile.WriteString(r.Name)
	}
}

/*  main
init the state and begin the go routines
*/
func main() {
	fmt.Println("[*] Started goGetBucket")

	/* collect args and die if there is an issue */
	s := parseArgs()
	if s == nil {
		panic("issue parsing args")
	}

	/* get starting time */
	start := time.Now()

	/* word channel to store words for bucket checking */
	inputChan := make(chan string, s.Threads)
	/* result channel to store output of bucket test function */
	resultChan := make(chan Result)

	/* create waitgroups for the threads */
	processorGroup := new(sync.WaitGroup)
	processorGroup.Add(s.Threads)
	/* create waitgroup for the printer */
	printerGroup := new(sync.WaitGroup)
	printerGroup.Add(1)

	scannerM, scannerW := bufio.NewScanner(nil), bufio.NewScanner(nil)

	/* open the desired files */
	if s.DomainName != "" {
		if s.MutateFileName == "" {
			panic("Domain provided but mutation file was not")
		}
		mutateList, err := os.Open(s.MutateFileName)
		if err != nil {
			panic("Failed to open mutation wordlist")
		}
		defer mutateList.Close()
		scannerM = bufio.NewScanner(mutateList)
	}

	if s.InputFileName != "" {
		wordlist, err := os.Open(s.InputFileName)
		if err != nil {
			panic("Failed to open wordlist")
		}
		defer wordlist.Close()
		scannerW = bufio.NewScanner(wordlist)
	}

	/* save time and just early exit */
	if scannerM == nil && scannerW == nil {
		panic("No wordlist and no mutationlist")
	}

	/* create the output file if it didn't already exist */
	if s.OutputFileName != "" {
		outputFile, err := os.Create(s.OutputFileName)
		if err != nil {
			panic("Unable to write to output file")
		} else {
			s.OutputFile = outputFile
			defer s.OutputFile.Close()
		}
	}

	fmt.Printf("[*] Starting %d threads..\n", s.Threads)
	/* create go-routines for all the threads */
	for i := 0; i < s.Threads; i++ {
		go func() {
			for word := range inputChan {
				/* process the bucket name with default region */
				checkBucket(s, word, resultChan, "us-west-2")
			}
			/* tell WG that the thread has finished */
			processorGroup.Done()
		}()
	}

	/* create single go routine to keep printing results as they arrive */
	go func() {
		for r := range resultChan {
			printResults(s, &r)
		}
		printerGroup.Done()
	}()

	/* store the input file contents in the inputChan */
	if s.DomainName != "" {
		fmt.Println("[*] Creating wordList from mutation file..")
		hostStr := strings.Split(s.DomainName, ".")[0]
		for scannerM.Scan() {
			word := strings.TrimSpace(scannerM.Text())
			/* perform mutation on domain and wordlist */
			for _, sep := range separators {
				inputChan <- (s.DomainName + sep + word)
				inputChan <- (word + sep + s.DomainName)
				inputChan <- (hostStr + sep + word)
				inputChan <- (word + sep + hostStr)
				s.Buckets = s.Buckets + 4
			}
		}
	}

	/* just import the input list for testing */
	if s.InputFileName != "" {
		fmt.Println("[*] Creating wordList from input file..")
		for scannerW.Scan() {
			word := strings.TrimSpace(scannerW.Text())
			/* use static input wordlist */
			if len(word) > 0 {
				inputChan <- word
				s.Buckets = s.Buckets + 1
			}
		}
	}

	fmt.Println("[*] Waiting on threads to complete..")

	close(inputChan)      /* we won't be adding more words */
	processorGroup.Wait() /* wait for all threads to finish */
	close(resultChan)     /* close the results chan input */
	printerGroup.Wait()   /* wait until results have all printed */

	/* print stats */
	elapsed := time.Since(start)
	fmt.Printf("\nCompleted %d requests in %s\n", s.Buckets, elapsed)
}
