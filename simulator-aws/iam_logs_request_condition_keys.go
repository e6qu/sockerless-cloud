package main

import "net/http"

func init() {
	registerIAMRequestConditionPopulator("logs", iamPopulateLogsRequestConditionKeys)
}

// iamPopulateLogsRequestConditionKeys adds the Amazon CloudWatch Logs keys for
// the resources a log delivery reads from and writes to.
func iamPopulateLogsRequestConditionKeys(_ *http.Request, operation string, body []byte, ctx map[string][]string) {
	var request struct {
		ResourceArn                      string `json:"resourceArn"`
		DeliveryDestinationConfiguration struct {
			DestinationResourceArn string `json:"destinationResourceArn"`
		} `json:"deliveryDestinationConfiguration"`
	}
	switch operation {
	case "PutDeliverySource", "PutDeliveryDestination":
	default:
		return
	}
	if !iamDecodeJSONRequest(body, &request) {
		return
	}
	if operation == "PutDeliverySource" {
		iamSetConditionValues(ctx, "logs:LogGeneratingResourceArns", request.ResourceArn)
		return
	}
	iamSetConditionValues(ctx, "logs:DeliveryDestinationResourceArn",
		request.DeliveryDestinationConfiguration.DestinationResourceArn)
}
