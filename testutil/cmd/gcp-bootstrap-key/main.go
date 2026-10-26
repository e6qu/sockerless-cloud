// Command gcp-bootstrap-key prints the Google Cloud simulator's bootstrap
// service-account credential file, whose token_uri names the simulator's token
// endpoint. Clients that read a credential file, such as gcloud, the client
// libraries and the Terraform provider, sign in with it as with a key IAM
// issued.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/e6qu/sockerless-cloud/testutil/gcpbootstrap"
)

func main() {
	tokenURI := flag.String("token-uri", "http://localhost:4567/token", "the simulator's token endpoint")
	out := flag.String("o", "", "write the file here instead of standard output")
	flag.Parse()
	if *out != "" {
		if err := gcpbootstrap.WriteCredentialFile(*out, *tokenURI); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Stdout.Write(gcpbootstrap.CredentialJSON(*tokenURI))
}
