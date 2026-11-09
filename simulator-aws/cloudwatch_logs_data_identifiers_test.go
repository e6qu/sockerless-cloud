package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cwDetected(t *testing.T, identifier, message string) []string {
	t.Helper()
	d, ok := cwManagedDataIdentifiers[identifier]
	require.True(t, ok, "managed data identifier %s", identifier)
	var out []string
	for _, sp := range d.find(message) {
		out = append(out, message[sp.start:sp.end])
	}
	return out
}

// TestCWManagedDataIdentifiersListTheGuidesARNs checks the catalogue against
// the data identifier ARNs the guide's type pages list.
func TestCWManagedDataIdentifiersListTheGuidesARNs(t *testing.T) {
	for _, name := range []string{
		"Address", "AwsSecretKey", "BankAccountNumber-DE", "BankAccountNumber-ES", "BankAccountNumber-FR", "BankAccountNumber-GB",
		"BankAccountNumber-IT", "BankAccountNumber-US", "CepCode-BR", "Cnpj-BR", "CpfCode-BR", "CreditCardExpiration",
		"CreditCardNumber", "CreditCardSecurityCode", "DriversLicense-AT", "DriversLicense-AU", "DriversLicense-BE",
		"DriversLicense-BG", "DriversLicense-CA", "DriversLicense-CY", "DriversLicense-CZ", "DriversLicense-DE",
		"DriversLicense-DK", "DriversLicense-EE", "DriversLicense-ES", "DriversLicense-FI", "DriversLicense-FR",
		"DriversLicense-GB", "DriversLicense-GR", "DriversLicense-HR", "DriversLicense-HU", "DriversLicense-IE",
		"DriversLicense-IT", "DriversLicense-LT", "DriversLicense-LU", "DriversLicense-LV", "DriversLicense-MT",
		"DriversLicense-NL", "DriversLicense-PL", "DriversLicense-PT", "DriversLicense-RO", "DriversLicense-SE",
		"DriversLicense-SI", "DriversLicense-SK", "DriversLicense-US", "DrugEnforcementAgencyNumber-US",
		"ElectoralRollNumber-GB", "EmailAddress", "HealthInsuranceCardNumber-EU", "HealthInsuranceClaimNumber-US",
		"HealthInsuranceNumber-FR", "HealthcareProcedureCode-US", "IndividualTaxIdentificationNumber-US", "InseeCode-FR",
		"IpAddress", "LatLong", "MedicareBeneficiaryNumber-US", "Name", "NationalDrugCode-US",
		"NationalIdentificationNumber-DE", "NationalIdentificationNumber-ES", "NationalIdentificationNumber-IT",
		"NationalInsuranceNumber-GB", "NationalProviderId-US", "NhsNumber-GB", "NieNumber-ES", "NifNumber-ES",
		"OpenSshPrivateKey", "PassportNumber-CA", "PassportNumber-DE", "PassportNumber-ES", "PassportNumber-FR",
		"PassportNumber-GB", "PassportNumber-IT", "PassportNumber-US", "PermanentResidenceNumber-CA",
		"PersonalHealthNumber-CA", "PgpPrivateKey", "PhoneNumber-BR", "PhoneNumber-DE", "PhoneNumber-ES", "PhoneNumber-FR",
		"PhoneNumber-GB", "PhoneNumber-IT", "PhoneNumber-US", "PkcsPrivateKey", "PostalCode-CA", "PuttyPrivateKey",
		"RgNumber-BR", "SocialInsuranceNumber-CA", "Ssn-ES", "Ssn-US", "TaxId-DE", "TaxId-ES", "TaxId-FR", "TaxId-GB",
		"VehicleIdentificationNumber", "ZipCode-US",
	} {
		_, ok := cwManagedDataIdentifiers[name]
		assert.True(t, ok, name)
	}
}

func TestCWManagedDataIdentifiersDetect(t *testing.T) {
	cases := []struct {
		identifier, message string
		want                []string
	}{
		{"EmailAddress", "contact jane.doe@example.com today", []string{"jane.doe@example.com"}},
		{"CreditCardNumber", "card 4532015112830366 charged", []string{"4532015112830366"}},
		{"CreditCardNumber", "number 4532015112830366 charged", nil},
		{"CreditCardNumber", "card 4111111111111111 charged", nil},
		{"CreditCardNumber", "card 4532015112830367 charged", nil},
		{"Ssn-US", "ssn: 123-45-6789", []string{"123-45-6789"}},
		{"Ssn-US", "ssn: 000-45-6789", nil},
		{"Ssn-US", "ssn                                         123-45-6789", nil},
		{"NhsNumber-GB", "NHS number 943 476 5919", []string{"943 476 5919"}},
		{"NhsNumber-GB", "NHS number 943 476 5918", nil},
		{"NationalIdentificationNumber-ES", "dni 12345678Z", []string{"12345678Z"}},
		{"NationalIdentificationNumber-ES", "dni 12345678A", nil},
		{"BankAccountNumber-DE", "iban DE89 3704 0044 0532 0130 00", []string{"DE89 3704 0044 0532 0130 00"}},
		{"BankAccountNumber-DE", "iban DE88 3704 0044 0532 0130 00", nil},
		{"DrugEnforcementAgencyNumber-US", "dea number AB1234563", []string{"AB1234563"}},
		{"CpfCode-BR", "cpf 529.982.247-25", []string{"529.982.247-25"}},
		{"PhoneNumber-US", "call +1 206 555 0100", []string{"+1 206 555 0100"}},
		{"PhoneNumber-US", "call 206 555 0100", nil},
		{"PhoneNumber-US", "phone: 206 555 0100", []string{"206 555 0100"}},
		{"IpAddress", "from 10.1.2.3 and 2001:db8::1", []string{"10.1.2.3", "2001:db8::1"}},
		{"IpAddress", "version 999.1.2.3", nil},
		{"Name", "signed by Jane Doe yesterday", []string{"Jane Doe"}},
		{"PkcsPrivateKey", "-----BEGIN RSA PRIVATE KEY-----\nMIIB\n-----END RSA PRIVATE KEY-----", []string{"-----BEGIN RSA PRIVATE KEY-----\nMIIB\n-----END RSA PRIVATE KEY-----"}},
		{"LatLong", "location 41.948614,-87.655311", []string{"41.948614,-87.655311"}},
		{"PostalCode-CA", "Toronto M5V 3L9", []string{"M5V 3L9"}},
		{"Address", "ship to 123 Main Street, Springfield, IL 62704 please", []string{"123 Main Street, Springfield, IL 62704"}},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, cwDetected(t, c.identifier, c.message), "%s in %q", c.identifier, c.message)
	}
}

// A keyword with a space also matches without it or with an underscore or a
// hyphen in its place.
func TestCWKeywordVariants(t *testing.T) {
	for _, message := range []string{
		"social security 123-45-6789", "social_security 123-45-6789", "social-security 123-45-6789", "socialsecurity 123-45-6789",
	} {
		assert.Equal(t, []string{"123-45-6789"}, cwDetected(t, "Ssn-US", message), message)
	}
}

func TestCWMaskKeepsEachCharacterPosition(t *testing.T) {
	msg := "é jane@example.com ü"
	spans := cwManagedDataIdentifiers["EmailAddress"].find(msg)
	assert.Equal(t, "é **************** ü", cwMask(msg, spans))
}

func TestCWCompileDataProtectionRefusesMalformedPolicies(t *testing.T) {
	statements := func(audit, deidentify string) string {
		return `{"Name":"p","Version":"2021-06-01","Statement":[` +
			`{"Sid":"a","DataIdentifier":[` + audit + `],"Operation":{"Audit":{"FindingsDestination":{}}}},` +
			`{"Sid":"d","DataIdentifier":[` + deidentify + `],"Operation":{"Deidentify":{"MaskConfig":{}}}}]}`
	}
	email := `"arn:aws:dataprotection::aws:data-identifier/EmailAddress"`
	_, err := cwCompileDataProtection(statements(email, email))
	require.NoError(t, err)
	for name, c := range map[string]struct{ doc, want string }{
		"mismatched identifiers": {statements(email, `"arn:aws:dataprotection::aws:data-identifier/IpAddress"`), "must match exactly"},
		"unknown identifier":     {statements(`"arn:aws:dataprotection::aws:data-identifier/Nope"`, `"arn:aws:dataprotection::aws:data-identifier/Nope"`), "unknown managed data identifier"},
		"undefined custom":       {statements(`"EmployeeId"`, `"EmployeeId"`), "neither a managed data identifier ARN nor a custom data identifier"},
		"one statement": {`{"Name":"p","Version":"2021-06-01","Statement":[{"DataIdentifier":[` + email + `],"Operation":{"Audit":{"FindingsDestination":{}}}}]}`,
			"exactly two statements"},
		"non-empty MaskConfig": {`{"Name":"p","Version":"2021-06-01","Statement":[` +
			`{"DataIdentifier":[` + email + `],"Operation":{"Audit":{"FindingsDestination":{}}}},` +
			`{"DataIdentifier":[` + email + `],"Operation":{"Deidentify":{"MaskConfig":{"x":1}}}}]}`, "empty MaskConfig"},
		"no FindingsDestination": {`{"Name":"p","Version":"2021-06-01","Statement":[` +
			`{"DataIdentifier":[` + email + `],"Operation":{"Audit":{}}},` +
			`{"DataIdentifier":[` + email + `],"Operation":{"Deidentify":{"MaskConfig":{}}}}]}`, "FindingsDestination"},
	} {
		_, err := cwCompileDataProtection(c.doc)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), c.want, name)
	}
}
