# goGetBucket - AWS S3 Bucket discovery through alterations and permutations

When performing a recon on a domain - understanding assets they own is very important. AWS S3 bucket permissions have been confused time and time again, and have allowed for the exposure of sensitive material.

What this tool does, is enumerate S3 bucket names using common patterns I have identified during my time bug hunting and pentesting. Permutations are supported on a root domain name using a custom wordlist. I highly recommend the one packaged within [AltDNS](https://github.com/infosec-au/altdns).

The following information about every bucket found to exist will be returned:
- List Permission
- Write Permission
- Region the Bucket exists in
- If the bucket has all access disabled

# Installation

```bash
git clone https://github.com/glen-mac/goGetBucket.git
cd goGetBucket
go get
go build
```

# Usage

`./goGetBucket -m ~/tools/altdns/words.txt -d <domain> -o <output> -i <wordlist>`


```
Usage of ./goGetBucket:
  -d string
        Supplied domain name (used with mutation flag)
  -f string
        Path to a testfile (default "/tmp/test.file")
  -i string
        Path to input wordlist to enumerate
  -k string
        Keyword list (used with mutation flag)
  -m string
        Path to mutation wordlist (requires domain flag)
  -o string
        Path to output file to store log
  -t int
        Number of concurrent threads (default 100)
```

Throughout my use of the tool, I have produced the best results when I feed in a list of subdomains for a root domain I am interested in. E.G:
```
www.domain.com
mail.domain.com
dev.domain.com
```

The test file (`-f`) is a file that the script will attempt to store in the bucket to test write permissions. So maybe store your contact information and a warning message if this is performed during a bounty?

The keyword list (`-k`) is concatenated with the root domain name (`-d`) and the domain without the TLD to permutate using the supplied permuation wordlist (`-m`).

Be sure not to increase the threads too high (`-t`) - as the AWS has API rate limiting that will kick in and start giving an undesired return code.

# Screenshots

<img src="https://i.imgur.com/ZeM5tzV.png">

# To-Do

- Write better GoLang
- Not use the AWS cli tool to perform write access checking
- Use net/http instead of the aws service libraries for go
- Optimize the region checking
