package main

import "net/http"

func init() {
	registerIAMRequestConditionPopulator("s3express", iamPopulateS3ExpressConditionKeys)
}

// iamPopulateS3ExpressConditionKeys adds the access-point keys s3express
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
func iamPopulateS3ExpressConditionKeys(r *http.Request, operation string, _ []byte, ctx map[string][]string) {
	switch operation {
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
