package main

import (
    "fmt"
    "flag"
    "bufio"
    "time"
    "sync"
    "os"
    "strings"
    "github.com/aws/aws-sdk-go/aws"
    "github.com/aws/aws-sdk-go/aws/session"
    "github.com/aws/aws-sdk-go/aws/awserr"
    "github.com/aws/aws-sdk-go/service/s3"
)

/* state of the system */
type State struct {
    InputFileName   string	/* the input wordlist */
    OutputFileName  string	/* the output file name */
    OutputFile	    *os.File	/* the output file handler */
    Threads	    int		/* the number of threads to use */
    Buckets         int         /* number of buckets to check */
}

/* state of bucket test return */
type Result struct {
    Name	string
    Region	string
    Status	bool
    Readable    bool
    Writeable   bool
}

/*  checkBucket
check if a bucket with a certain name exists
*/
func checkBucket(s *State, name string, resultChan chan<- Result) {
    s.Buckets = s.Buckets+1
    s3svc := s3.New(session.New(), aws.NewConfig().WithRegion("us-west-2"))
    lor := &s3.ListObjectsInput{
	Bucket:		aws.String(name),
	MaxKeys:	aws.Int64(0),
    }
    _, err := s3svc.ListObjects(lor)
    r := Result{
	Name: name,
	Region: "us-west-2",
	Status: (err == nil),
	Readable: false,
	Writeable: false,
    }

    if err != nil {
	if awsErr, ok := err.(awserr.Error); ok {
	    switch awsErr.Code() {
	    case "NoSuchBucket":
		return
		//fmt.Printf("%s: not found\n", name)
	    case "BucketRegionError":
		fmt.Printf("%s found but wrong region\n", name)
		return
                r.Region = discoverRegion(s3svc, name)
		resultChan <- r
	    case "RequestLimitExceeded":
		fmt.Println("rate limit exceeded")
	    default:
		fmt.Println("%s default error\n", name)
	    }
	}
    } else {
	resultChan <- r
    }
}

/* discoverRegion
the point of this function is to find the region in which this bucket belongs
*/
func discoverRegion(s3svc *s3.S3, bucket string) string {
    /* check if we have permissions to use the get bucket region endpoint */
    ctx := &s3.GetBucketLocationInput{
	Bucket: aws.String(bucket),
    }
    region, err := s3svc.GetBucketLocation(ctx)
    if err != nil {
	if aerr, ok := err.(awserr.Error); ok && aerr.Code()=="AccessDenied" {
	    regionList := []string{"us-east-2", "us-east-1", "us-west-1", "us-west-2",
	    "ca-central-1", "ap-south-1", "ap-northeast-2", "ap-southeast-1",
	    "ap-southeast-2", "ap-northeast-1", "eu-central-1", "eu-west-1",
	    "eu-west-2", "sa-east-1"}
	    lor := &s3.ListObjectsInput{
		Bucket:		aws.String(bucket),
		MaxKeys:	aws.Int64(0),
	    }
	    for _, region := range regionList {
		s3svc := s3.New(session.New(), aws.NewConfig().WithRegion(region))
		_, err := s3svc.ListObjects(lor)
		if (err==nil){
		    return region
		}
	    }
	}
	return "error"
    }
    return *region.LocationConstraint
}

/*  parseArgs
parse the command line arguments and store the state to use throughout
the program usage
*/
func parseArgs() *State {
    s := new(State)
    flag.IntVar(&s.Threads, "t", 10, "Number of concurrent threads")
    flag.StringVar(&s.InputFileName, "i", "", "Number of concurrent threads")
    flag.StringVar(&s.OutputFileName, "o", "", "Number of concurrent threads")
    flag.BooleanVar(&s.RegionCheck, "o", "", "Number of concurrent threads")
    flag.Parse()
    return s
}

func printResults(s *State, r *Result) {
    fmt.Printf("Bucket: %s in region %s has R/W: %t/%t\n", r.Name, r.Region, r.Readable, r.Writeable)
}

/*  main
init the state and begin the go routines
*/
func main() {
    fmt.Println("[*] Started goGetBucket")
    s := parseArgs()
    if (s == nil) {
	return
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

    /* open the input file */
    wordlist, err := os.Open(s.InputFileName)
    if err != nil {
	panic("Failed to open wordlist")
    }
    defer wordlist.Close()
    scanner := bufio.NewScanner(wordlist)

    /* create the output file if it didn't already exist */
    /* var outputFile *os.File
    if s.OutputFileName != "" {
	outputFile, err := os.Create(s.OutputFileName)
	if err != nil {
	    fmt.Printf("[!] Unable to write to %s, falling back to stdout.\n", s.OutputFileName)
	    s.OutputFileName = ""
	    s.OutputFile = nil
	} else {
	    s.OutputFile = outputFile
	}
    }*/

    /* create go-routines for all the threads */
    for i := 0; i < s.Threads; i++ {
	go func() {
	    for word := range inputChan {
		/* process the word */
		checkBucket(s, word, resultChan)
	    }
	    /* tell WG that the thread has finished */
	    processorGroup.Done()
	}()
    }

    /* create go routine to keep printing results as they arrive */
    go func() {
	for r := range resultChan {
	    printResults(s, &r)
	}
	printerGroup.Done()
    }()

    /* store the input file contents in the inputChan */
    for scanner.Scan() {
	word := strings.TrimSpace(scanner.Text())
	if len(word) > 0 {
	    inputChan <- word
	}
    }

    close(inputChan)		/* we won't be adding more words */
    processorGroup.Wait()	/* wait for all threads to finish */
    close(resultChan)		/* close the results chan input */
    printerGroup.Wait()		/* wait until results have all printed */
    /*if s.OutputFile != nil {
	outputFile.Close()
    }*/
    elapsed := time.Since(start)
    fmt.Printf("\nCompleted %d requests in %s\n", s.Buckets, elapsed)
}