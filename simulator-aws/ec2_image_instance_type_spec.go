package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// An AMI's instance type specification names which instance types may launch
// it. RunInstances validates against it, so the specification is not a label:
// storing it without enforcing it would report a restriction the simulator does
// not apply.

// EC2ImageInstanceTypeSpec is an AMI's instance type specification. No record
// means every instance type is allowed.
type EC2ImageInstanceTypeSpec struct {
	ImageId                  string
	SupportedInstanceTypes   []string
	UnsupportedInstanceTypes []string
}

var ec2ImageInstanceTypeSpecs sim.Store[EC2ImageInstanceTypeSpec]

func registerEC2ImageInstanceTypeSpec(r *AWSQueryRouter, srv *sim.Server) {
	ec2ImageInstanceTypeSpecs = sim.MakeStore[EC2ImageInstanceTypeSpec](srv.DB(), "ec2_image_instance_type_specs")
	r.Register("ReplaceImageInstanceTypeSpecification", handleReplaceImageInstanceTypeSpecification)
}

// ec2InstanceTypePatternMatches evaluates one entry, which may end in `*`
// (`t3.*` matches every t3 size).
func ec2InstanceTypePatternMatches(pattern, instanceType string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(instanceType, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == instanceType
}

func ec2InstanceTypeMatchesAny(patterns []string, instanceType string) bool {
	for _, p := range patterns {
		if ec2InstanceTypePatternMatches(p, instanceType) {
			return true
		}
	}
	return false
}

// ec2ImageAllowsInstanceType applies the evaluation order the API documents:
// no specification allows everything; unsupported alone excludes its matches;
// supported requires a match and still excludes an unsupported one.
func ec2ImageAllowsInstanceType(imageID, instanceType string) bool {
	spec, ok := ec2ImageInstanceTypeSpecs.Get(imageID)
	if !ok {
		return true
	}
	if ec2InstanceTypeMatchesAny(spec.UnsupportedInstanceTypes, instanceType) {
		return false
	}
	if len(spec.SupportedInstanceTypes) == 0 {
		return true
	}
	return ec2InstanceTypeMatchesAny(spec.SupportedInstanceTypes, instanceType)
}

// ec2ImageInstanceTypeSpecXML renders the member DescribeImages carries.
func ec2ImageInstanceTypeSpecXML(imageID string) string {
	spec, ok := ec2ImageInstanceTypeSpecs.Get(imageID)
	if !ok {
		return ""
	}
	// The request carries bare strings; the response wraps each in an
	// InstanceTypeItem, so an item holds <instanceType> rather than the value.
	set := func(name string, values []string) string {
		if len(values) == 0 {
			return ""
		}
		var b strings.Builder
		fmt.Fprintf(&b, "<%s>", name)
		for _, v := range values {
			fmt.Fprintf(&b, "<item><instanceType>%s</instanceType></item>", xmlEscape(v))
		}
		fmt.Fprintf(&b, "</%s>", name)
		return b.String()
	}
	return "<instanceTypeSpecification>" +
		set("supportedInstanceTypeSet", spec.SupportedInstanceTypes) +
		set("unsupportedInstanceTypeSet", spec.UnsupportedInstanceTypes) +
		"</instanceTypeSpecification>"
}

func handleReplaceImageInstanceTypeSpecification(w http.ResponseWriter, r *http.Request) {
	imageID := r.FormValue("ImageId")
	if imageID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ImageId", http.StatusBadRequest)
		return
	}
	img, ok := ec2Images.Get(imageID)
	if !ok {
		ec2ErrorXML(w, "InvalidAMIID.NotFound", fmt.Sprintf("The image id %q does not exist", imageID), http.StatusBadRequest)
		return
	}
	if img.OwnerId != awsAccountID() {
		ec2ErrorXML(w, "AuthFailure",
			fmt.Sprintf("You do not own the image %q", imageID), http.StatusBadRequest)
		return
	}

	// The list flattens to a singular key — `SupportedInstanceType.1` — which
	// is the name the model's list member carries, not the plural struct member.
	supported := ec2CriterionValues(r, "InstanceTypeSpecification.SupportedInstanceType")
	unsupported := ec2CriterionValues(r, "InstanceTypeSpecification.UnsupportedInstanceType")

	// Omitting the specification removes it, which is how the caller restores
	// an AMI any instance type may launch.
	if len(supported) == 0 && len(unsupported) == 0 {
		ec2ImageInstanceTypeSpecs.Delete(imageID)
	} else {
		ec2ImageInstanceTypeSpecs.Put(imageID, EC2ImageInstanceTypeSpec{
			ImageId:                  imageID,
			SupportedInstanceTypes:   supported,
			UnsupportedInstanceTypes: unsupported,
		})
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ReplaceImageInstanceTypeSpecificationResponse %s><requestId>%s</requestId><return>true</return></ReplaceImageInstanceTypeSpecificationResponse>`,
		ec2Xmlns(), generateUUID())
}
