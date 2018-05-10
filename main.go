package main

import (
	"bufio"
	"flag"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/fatih/color"
	"github.com/satori/go.uuid"
	"io/ioutil"
	"net/http"
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
	WriteTestFile  *os.File /* the writable test file handler */
	Threads        int      /* the number of threads to use */
	MutateFileName string   /* the mutation file name */
	DomainName     string   /* the name of the domain */
	KeywordList    string   /* list of keywords */
	Buckets        int      /* the number of buckets read */
	AwsBin         string   /* location of aws cli binary */
}

/* state of bucket test return */
type Result struct {
	Name     string /* bucket name */
	Region   string /* bucket region */
	Status   bool   /* was there an error? */
	Listable bool   /* bucket listable status */
	Writable bool   /* bucket writeable status */
}

var baseUrlAWS = []string{"http://s3-1-w.amazonaws.com", ".s3.amazonaws.com"}
var regionListAWS = []string{
	"us-east-2", "us-east-1", "us-west-1", "us-west-2", "ca-central-1",
	"ap-south-1", "ap-northeast-2", "ap-southeast-1", "ap-southeast-2",
	"ap-northeast-1", "eu-central-1", "eu-west-1", "eu-west-2", "sa-east-1"}

var separators = []string{".", "-", ""}

/*
check if a bucket with a certain name exists using Go HTTP routines.
most of this code is taken from https://github.com/bbb31/slurp
because i'm not a go coder at all.

this is useful to reduce api calls to aws, while still trying our luck
for forbidden buckets when necessary. in fact, many buckets will have
read access on auth sessions, while denying it to public http clients.
*/
func checkBucketHTTP(s *State, bucket string, resultChan chan<- Result) {
	tr := &http.Transport{
		IdleConnTimeout:       3 * time.Second,
		ResponseHeaderTimeout: 3 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	redirect := false
	// HEAD requests trigger a ratelimit, stick to GET
	req, err := http.NewRequest("GET", baseUrlAWS[0], nil)

	if err != nil {
		return
	}

	req.Host = bucket + baseUrlAWS[1]

	// don't look at me like that
perform_request:
	resp, err := client.Do(req)
	if err != nil {
		return
	}

	defer resp.Body.Close()

	r := Result{
		Name:     bucket,
		Region:   "",
		Status:   err == nil,
		Listable: true,
		Writable: false,
	}

	switch resp.StatusCode {
	// bucket exists and is listable, check writing permissions
	case 200:
		r.Region = discoverRegion(bucket)
		checkWritableAWS(&r, s)
		resultChan <- r

	// bucket is somewhere else, follow the redirect
	case 307:
		if redirect == true {
			// don't get caught in a loop...
			return
		}
		loc := resp.Header.Get("Location")
		req, err = http.NewRequest("GET", loc, nil)
		// we all have our dirty secrets, don't look at me like that
		redirect = true
		goto perform_request

	// bucket exists and is not listable: try again with an auth session
	case 403:
		checkBucketAWS(s, bucket, resultChan, "us-west-2")

	// bucket does not exist
	case 404:
		return

	// rate limit exceeded
	case 503:
		fmt.Println("rate limit exceeded! consider reducing threads")
	}
}

/*
check if a bucket with a certain name exists using the aws s3 sdk routines
*/
func checkBucketAWS(s *State, bucket string, resultChan chan<- Result, region string) {
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
		Name:     bucket,
		Region:   region,
		Status:   err == nil,
		Listable: true,
		Writable: false,
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
					checkBucketAWS(s, bucket, resultChan, foundRegion)
				}
			case "RequestLimitExceeded":
				fmt.Println("rate limit exceeded! consider reducing threads")
			case "AccessDenied":
				r.Listable = false
				checkWritableAWS(&r, s)
				resultChan <- r
			}
		}
	} else {
		/* bucket exists and is listable */
		checkWritableAWS(&r, s)
		resultChan <- r
	}
}

/*
check if the bucket is writeable using the AWS S3 SDK routines
*/
func checkWritableAWS(r *Result, s *State) {
	/* setup session */
	conf := aws.Config{Region: aws.String(r.Region)}
	sess := session.New(&conf)
	svc := s3manager.NewUploader(sess)

	/* perform upload */
	_, err := svc.Upload(&s3manager.UploadInput{
		Bucket: aws.String(r.Name),
		Key:    aws.String(uuid.Must(uuid.NewV4()).String()),
		Body:   s.WriteTestFile,
	})
	if err == nil {
		r.Writable = true
	}
}

/*
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
			for _, region := range regionListAWS {
				s3svc = s3.New(session.New(), aws.NewConfig().WithRegion(region))
				_, err := s3svc.ListObjects(lor)
				if err == nil || err.(awserr.Error).Code() == "AccessDenied" {
					return region
				}
			}
			return "no_region_found"
		} else {
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
	return region
}

/*
parse the command line arguments and store the state to use throughout program usage
*/
func parseArgs() *State {
	s := new(State)
	flag.IntVar(&s.Threads, "t", 100, "Number of concurrent threads")
	flag.StringVar(&s.InputFileName, "i", "", "Path to input wordlist to enumerate")
	flag.StringVar(&s.OutputFileName, "o", "", "Path to output file to store log")
	flag.StringVar(&s.MutateFileName, "m", "", "Path to mutation wordlist (requires domain flag)")
	flag.StringVar(&s.DomainName, "d", "", "Supplied domain name (used with mutation flag)")
	flag.StringVar(&s.KeywordList, "k", "", "Keyword list (used with mutation flag)")
	flag.Parse()
	return s
}

/*
parse a result object and print it in a desired manner
*/
func printResults(s *State, r *Result) {
	listable := color.RedString("R")
	writeable := color.RedString("W")
	if r.Listable {
		listable = color.GreenString("R")
	}
	if r.Writable {
		writeable = color.GreenString("W")
	}
	fmt.Printf("Bucket: %s/%s %s\n", listable, writeable, color.BlueString(r.Name))
	if s.OutputFile != nil {
		outputStr := fmt.Sprintf("%s,%s,%t,%t\n", r.Name, r.Region, r.Listable, r.Writable)
		s.OutputFile.WriteString(outputStr)
	}
}

/*
init the state and begin the go routines
*/
func main() {
	fmt.Println("[*] Started goGetBucket")

	/* collect args and die if there is an issue */
	s := parseArgs()
	if s == nil {
		panic("issue parsing args")
	}

	/* open the test file for write permissions checking */
	testFile, err := ioutil.TempFile("", "goGetBucket")
	if err != nil {
		panic("Failed to open write permissions test file")
	}
	s.WriteTestFile = testFile
	defer testFile.Close()
	defer os.Remove(testFile.Name())

	/* get starting time */
	start := time.Now()

	/* channel to store bucket permutations */
	inputChan := make(chan string, s.Threads)
	/* channel to store bucket check results */
	resultChan := make(chan Result)

	/* create waitgroups for the threads */
	processorGroup := new(sync.WaitGroup)
	processorGroup.Add(s.Threads)

	/* create waitgroup for the printer */
	printerGroup := new(sync.WaitGroup)
	printerGroup.Add(1)

	/* count the number of input sources */
	numInputSources := 0

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
		numInputSources++
	}

	if s.InputFileName != "" {
		wordlist, err := os.Open(s.InputFileName)
		if err != nil {
			panic("Failed to open wordlist")
		}
		defer wordlist.Close()
		scannerW = bufio.NewScanner(wordlist)
		numInputSources++
	}

	/* save time and just early exit */
	if scannerM == nil && scannerW == nil {
		panic("No wordlist and no mutationlist")
	}

	/* create waitgroup for the printer */
	inputFileGroup := new(sync.WaitGroup)
	inputFileGroup.Add(numInputSources)

	/* create the output file if it didn't already exist */
	if s.OutputFileName != "" {
		outputFile, err := os.Create(s.OutputFileName)
		if err != nil {
			panic("Unable to write to output file")
		}
		s.OutputFile = outputFile
		defer outputFile.Close()
	}

	fmt.Printf("[*] Starting %d checking threads..\n", s.Threads)
	/* create go-routines for all the threads */
	for i := 0; i < s.Threads; i++ {
		go func() {
			for word := range inputChan {
				/* process the bucket name with default region */
				checkBucketHTTP(s, word, resultChan)
			}
			/* tell WG that the thread has finished */
			processorGroup.Done()
		}()
	}

	/* store the input file contents in the inputChan */
	if s.DomainName != "" {
		go func() {
			fmt.Println("[*] Creating wordList from mutation file..")
			hostStr := strings.Split(s.DomainName, ".")[0]
			stringList := strings.Split(s.KeywordList, " ")
			if s.KeywordList == "" {
				stringList = []string{}
			}

			inputChan <- hostStr
			inputChan <- s.DomainName
			s.Buckets = s.Buckets + 2

			for scannerM.Scan() {
				word := strings.TrimSpace(scannerM.Text())
				for _, sep := range separators {
					inputChan <- hostStr + sep + word
					inputChan <- word + sep + hostStr
					inputChan <- s.DomainName + sep + word
					inputChan <- word + sep + s.DomainName
					s.Buckets = s.Buckets + 4

					for _, keyword := range stringList {
						inputChan <- hostStr + sep + word + sep + keyword
						inputChan <- hostStr + sep + keyword + sep + word
						inputChan <- word + sep + hostStr + sep + keyword
						inputChan <- word + sep + keyword + sep + hostStr
						inputChan <- keyword + sep + hostStr + sep + word
						inputChan <- keyword + sep + word + sep + hostStr
						inputChan <- s.DomainName + sep + word + sep + keyword
						inputChan <- s.DomainName + sep + keyword + sep + word
						inputChan <- word + sep + s.DomainName + sep + keyword
						inputChan <- word + sep + keyword + sep + s.DomainName
						inputChan <- keyword + sep + s.DomainName + sep + word
						inputChan <- keyword + sep + word + sep + s.DomainName
						s.Buckets = s.Buckets + 12
					}
				}
			}
			inputFileGroup.Done()
		}()
	}

	/* just import the input list for testing */
	if s.InputFileName != "" {
		go func() {
			fmt.Println("[*] Creating wordList from input file..")
			for scannerW.Scan() {
				word := strings.TrimSpace(scannerW.Text())
				/* use static input wordlist */
				if len(word) > 0 {
					inputChan <- word
					s.Buckets = s.Buckets + 1
				}
			}
			inputFileGroup.Done()
		}()
	}

	/* wait for the input files to finish loading before we print output */
	inputFileGroup.Wait()

	fmt.Println("[*] Waiting on permutator threads to complete..\n")

	/* create single go routine to keep printing results as they arrive */
	go func() {
		for r := range resultChan {
			printResults(s, &r)
		}
		printerGroup.Done()
	}()

	close(inputChan)      /* we won't be adding more words */
	processorGroup.Wait() /* wait for all permutation threads to finish */
	close(resultChan)     /* close the results chan input */
	printerGroup.Wait()   /* wait until results have all printed */

	/* print stats */
	elapsed := time.Since(start)
	fmt.Printf("\nCompleted %d requests in %s\n", s.Buckets, elapsed)
}
