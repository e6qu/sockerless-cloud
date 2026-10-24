package main

import (
	"encoding/xml"
	"net/http"
)

func init() {
	registerIAMRequestConditionPopulator("s3express", iamPopulateS3ExpressConditionKeys)
}

// iamPopulateS3ExpressConditionKeys adds the mode and encryption a
// CreateSession request asks for, the zone a directory bucket's CreateBucket
// places it in, and the access-point keys s3express
// declares on the scope operations this simulator serves. AWS declares
// s3express:DataAccessPointArn, s3express:DataAccessPointAccount and
// s3express:AccessPointNetworkOrigin on PutAccessPointScope,
// GetAccessPointScope and DeleteAccessPointScope, and a scope request names
// its access point in the path.
//
// The ARN is the s3express one the reference declares for its accesspoint
// resource type, not the s3 access-point ARN, so a policy over a directory
// bucket's access points is not satisfied by one written over the
// general-purpose ones. A request naming an access point the simulator does
// not hold settles nothing: there is no access point to describe.
func iamPopulateS3ExpressConditionKeys(r *http.Request, operation string, body []byte, ctx map[string][]string) {
	switch operation {
	case "CreateSession":
		// A request that names no mode asks for ReadWrite.
		mode := r.Header.Get("x-amz-create-session-mode")
		if mode == "" {
			mode = "ReadWrite"
		}
		iamSetConditionValues(ctx, "s3express:SessionMode", mode)
		// The encryption the session's writes use, from the headers the
		// model binds CreateSession's ServerSideEncryption and SSEKMSKeyId to.
		iamSetConditionValues(ctx, "s3express:x-amz-server-side-encryption", r.Header.Get("x-amz-server-side-encryption"))
		iamSetConditionValues(ctx, "s3express:x-amz-server-side-encryption-aws-kms-key-id",
			r.Header.Get("x-amz-server-side-encryption-aws-kms-key-id"))
		return
	case "CreateBucket":
		// The Availability Zone or Local Zone id the directory bucket is
		// placed in, which CreateBucketConfiguration names as its Location.
		var configuration struct {
			Location struct {
				Name string `xml:"Name"`
			} `xml:"Location"`
		}
		if len(body) > 0 && xml.Unmarshal(body, &configuration) == nil {
			iamSetConditionValues(ctx, "s3express:LocationName", configuration.Location.Name)
		}
		return
	case "PutInventoryConfiguration":
		var configuration struct {
			Fields []string `xml:"OptionalFields>Field"`
		}
		if len(body) > 0 && xml.Unmarshal(body, &configuration) == nil {
			iamSetConditionValues(ctx, "s3express:InventoryAccessibleOptionalFields", configuration.Fields...)
		}
		return
	case "PutAccessPointScope", "GetAccessPointScope", "DeleteAccessPointScope":
	default:
		return
	}
	arn := s3ExpressAccessPointResource(r)
	if arn == "" {
		return
	}
	account := s3ControlAccountID(r)
	ap, ok := s3AccessPoints.Get(s3AccessPointKey(account, r.PathValue("name")))
	if !ok {
		return
	}
	iamSetConditionValues(ctx, "s3express:DataAccessPointArn", arn)
	iamSetConditionValues(ctx, "s3express:DataAccessPointAccount", ap.AccountID)
	iamSetConditionValues(ctx, "s3express:AccessPointNetworkOrigin", s3AccessPointNetworkOrigin(ap))
}
