package aws_sdk_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGlueBusinessContextLifecycle_SDK(t *testing.T) {
	client := glueClient()

	formType, err := client.PutFormType(ctx, &glue.PutFormTypeInput{
		Name:   aws.String("BusinessMetadata"),
		Schema: aws.String("structure BusinessMetadata { owner: String }"),
	})
	require.NoError(t, err)
	assert.Equal(t, "BusinessMetadata", aws.ToString(formType.Id))

	gotFormType, err := client.GetFormType(ctx, &glue.GetFormTypeInput{Identifier: formType.Id})
	require.NoError(t, err)
	assert.Equal(t, "BusinessMetadata", aws.ToString(gotFormType.Name))
	formTypes, err := client.ListFormTypes(ctx, &glue.ListFormTypesInput{MaxResults: aws.Int32(1)})
	require.NoError(t, err)
	require.NotEmpty(t, formTypes.Items)

	assetType, err := client.PutAssetType(ctx, &glue.PutAssetTypeInput{
		Name: aws.String("BusinessDataSet"),
		Forms: map[string]gluetypes.AssetTypeFormReference{
			"metadata": {FormTypeIdentifier: formType.Id},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "BusinessDataSet", aws.ToString(assetType.Id))

	gotAssetType, err := client.GetAssetType(ctx, &glue.GetAssetTypeInput{Identifier: assetType.Id})
	require.NoError(t, err)
	require.Contains(t, gotAssetType.Forms, "metadata")
	assetTypes, err := client.ListAssetTypes(ctx, &glue.ListAssetTypesInput{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(assetTypes.Items), 2)

	asset, err := client.PutAsset(ctx, &glue.PutAssetInput{
		AssetTypeId: assetType.Id,
		Identifier:  aws.String("quarterly-sales"),
		Name:        aws.String("Quarterly Sales"),
		Description: aws.String("Revenue data for the quarter"),
		Forms: map[string]gluetypes.AssetFormEntry{
			"metadata": {FormTypeId: formType.Id, Content: aws.String(`{"owner":"finance"}`)},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "quarterly-sales", aws.ToString(asset.Id))

	_, err = client.PutAttachment(ctx, &glue.PutAttachmentInput{
		AssetIdentifier: aws.String("quarterly-sales"),
		AttachmentName:  aws.String("review"),
		FormTypeId:      formType.Id,
		Content:         aws.String(`{"owner":"controller"}`),
	})
	require.NoError(t, err)

	glossary, err := client.CreateGlossary(ctx, &glue.CreateGlossaryInput{
		Name:        aws.String("Finance Terms"),
		Description: aws.String("Controlled finance vocabulary"),
		ClientToken: aws.String("finance-glossary-token"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(glossary.Id))

	idempotentGlossary, err := client.CreateGlossary(ctx, &glue.CreateGlossaryInput{
		Name:        aws.String("Finance Terms"),
		Description: aws.String("Controlled finance vocabulary"),
		ClientToken: aws.String("finance-glossary-token"),
	})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(glossary.Id), aws.ToString(idempotentGlossary.Id))

	gotGlossary, err := client.GetGlossary(ctx, &glue.GetGlossaryInput{Identifier: glossary.Id})
	require.NoError(t, err)
	assert.Equal(t, "Finance Terms", aws.ToString(gotGlossary.Name))
	glossaries, err := client.ListGlossaries(ctx, &glue.ListGlossariesInput{})
	require.NoError(t, err)
	require.Len(t, glossaries.Items, 1)

	term, err := client.CreateGlossaryTerm(ctx, &glue.CreateGlossaryTermInput{
		GlossaryIdentifier: glossary.Id,
		Name:               aws.String("Net Revenue"),
		ShortDescription:   aws.String("Revenue after deductions"),
		LongDescription:    aws.String("Gross revenue less returns, allowances, and discounts"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(term.Id))

	gotTerm, err := client.GetGlossaryTerm(ctx, &glue.GetGlossaryTermInput{Identifier: term.Id})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(glossary.Id), aws.ToString(gotTerm.GlossaryId))
	terms, err := client.ListGlossaryTerms(ctx, &glue.ListGlossaryTermsInput{GlossaryIdentifier: glossary.Id})
	require.NoError(t, err)
	require.Len(t, terms.Items, 1)

	rawListBody, err := json.Marshal(map[string]any{"GlossaryIdentifier": aws.ToString(glossary.Id)})
	require.NoError(t, err)
	rawListRequest, err := http.NewRequest(http.MethodPost, baseURL+"/", bytes.NewReader(rawListBody))
	require.NoError(t, err)
	rawListRequest.Header.Set("X-Amz-Target", "AWSGlue.ListGlossaryTerms")
	rawListRequest.Header.Set("Content-Type", "application/x-amz-json-1.1")
	signRawSigV4JSON(t, rawListRequest, "glue", rawListBody)
	rawListResponse, err := http.DefaultClient.Do(rawListRequest)
	require.NoError(t, err)
	defer rawListResponse.Body.Close()
	require.Equal(t, http.StatusOK, rawListResponse.StatusCode)
	var rawListOutput map[string]json.RawMessage
	require.NoError(t, json.NewDecoder(rawListResponse.Body).Decode(&rawListOutput))
	assert.Contains(t, rawListOutput, "Items")
	assert.NotContains(t, rawListOutput, "GlossaryId")

	associated, err := client.AssociateGlossaryTerms(ctx, &glue.AssociateGlossaryTermsInput{
		AssetIdentifier:         aws.String("quarterly-sales"),
		GlossaryTermIdentifiers: []string{aws.ToString(term.Id)},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{aws.ToString(term.Id)}, associated.GlossaryTerms)

	gotAsset, err := client.GetAsset(ctx, &glue.GetAssetInput{Identifier: aws.String("quarterly-sales")})
	require.NoError(t, err)
	assert.Equal(t, "BusinessDataSet", aws.ToString(gotAsset.AssetTypeId))
	require.Contains(t, gotAsset.Attachments, "review")
	assert.Equal(t, []string{aws.ToString(term.Id)}, gotAsset.GlossaryTerms)

	updatedAsset, err := client.UpdateAsset(ctx, &glue.UpdateAssetInput{
		Identifier:  aws.String("quarterly-sales"),
		Name:        aws.String("Quarterly Net Sales"),
		Description: aws.String("Updated revenue data"),
	})
	require.NoError(t, err)
	assert.Equal(t, "Quarterly Net Sales", aws.ToString(updatedAsset.Name))
	require.NotNil(t, updatedAsset.UpdatedAt)

	search, err := client.SearchAssets(ctx, &glue.SearchAssetsInput{
		SearchText: aws.String("net sales"),
		FilterClause: &gluetypes.SearchFilterClauseMemberAttributeFilter{
			Value: gluetypes.SearchAttributeFilter{
				Attribute: aws.String("AssetTypeId"),
				Operator:  gluetypes.SearchFilterOperatorEquals,
				Value: &gluetypes.SearchFilterValueMemberStringValue{
					Value: "BusinessDataSet",
				},
			},
		},
		Sort: &gluetypes.SearchSort{
			Attribute: aws.String("name"),
			Order:     gluetypes.SearchSortOrderAscending,
		},
	})
	require.NoError(t, err)
	require.Len(t, search.Items, 1)
	assert.Equal(t, "quarterly-sales", aws.ToString(search.Items[0].Id))

	// This asset names no Data Catalog object, so it carries no iterable form
	// and both readers answer with the service's not-found error. The error code
	// is asserted rather than the mere presence of an error, so a handler that
	// failed for any other reason — an unrouted operation, a decode failure —
	// would not pass for it. The reachable side of the surface, an asset that
	// names a table and so carries its columns, is
	// TestGlueIterableFormsOfATableAsset_SDK.
	_, err = client.ListIterableForms(ctx, &glue.ListIterableFormsInput{
		AssetIdentifier:  aws.String("quarterly-sales"),
		IterableFormName: aws.String("columns"),
	})
	assert.Equal(t, "EntityNotFoundException", errCode(t, err))
	_, err = client.BatchGetIterableForms(ctx, &glue.BatchGetIterableFormsInput{
		AssetIdentifier:  aws.String("quarterly-sales"),
		IterableFormName: aws.String("columns"),
		ItemIdentifiers:  []string{"amount"},
	})
	assert.Equal(t, "EntityNotFoundException", errCode(t, err))

	// The asset itself must be the other half of that decision: an asset that
	// does not exist is reported against the asset, not the form.
	_, err = client.ListIterableForms(ctx, &glue.ListIterableFormsInput{
		AssetIdentifier:  aws.String("no-such-asset"),
		IterableFormName: aws.String("columns"),
	})
	assert.Equal(t, "EntityNotFoundException", errCode(t, err))

	_, err = client.DeleteAttachment(ctx, &glue.DeleteAttachmentInput{
		AssetIdentifier: aws.String("quarterly-sales"),
		AttachmentName:  aws.String("review"),
	})
	require.NoError(t, err)
	disassociated, err := client.DisassociateGlossaryTerms(ctx, &glue.DisassociateGlossaryTermsInput{
		AssetIdentifier:         aws.String("quarterly-sales"),
		GlossaryTermIdentifiers: []string{aws.ToString(term.Id)},
	})
	require.NoError(t, err)
	assert.Empty(t, disassociated.GlossaryTerms)

	updatedGlossary, err := client.UpdateGlossary(ctx, &glue.UpdateGlossaryInput{
		Identifier:  glossary.Id,
		Description: aws.String("Updated controlled finance vocabulary"),
	})
	require.NoError(t, err)
	assert.Equal(t, "Updated controlled finance vocabulary", aws.ToString(updatedGlossary.Description))
	updatedTerm, err := client.UpdateGlossaryTerm(ctx, &glue.UpdateGlossaryTermInput{
		Identifier:       term.Id,
		ShortDescription: aws.String("Revenue net of deductions"),
	})
	require.NoError(t, err)
	assert.Equal(t, "Revenue net of deductions", aws.ToString(updatedTerm.ShortDescription))

	_, err = client.DeleteGlossaryTerm(ctx, &glue.DeleteGlossaryTermInput{Identifier: term.Id})
	require.NoError(t, err)
	_, err = client.DeleteGlossary(ctx, &glue.DeleteGlossaryInput{Identifier: glossary.Id})
	require.NoError(t, err)
	_, err = client.DeleteAsset(ctx, &glue.DeleteAssetInput{Identifier: aws.String("quarterly-sales")})
	require.NoError(t, err)
	_, err = client.DeleteAssetType(ctx, &glue.DeleteAssetTypeInput{Identifier: assetType.Id})
	require.NoError(t, err)
	_, err = client.DeleteFormType(ctx, &glue.DeleteFormTypeInput{Identifier: formType.Id})
	require.NoError(t, err)
}

func TestGlueEntityRecordsAndBatchEvaluationRuns_SDK(t *testing.T) {
	client := glueClient()
	dynamoDBClient := ddbClient()

	_, err := dynamoDBClient.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String("glue-business-entities"),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("id"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("id"), KeyType: ddbtypes.KeyTypeHash},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)
	for _, item := range []map[string]ddbtypes.AttributeValue{
		{
			"id":     &ddbtypes.AttributeValueMemberS{Value: "1"},
			"region": &ddbtypes.AttributeValueMemberS{Value: "emea"},
			"amount": &ddbtypes.AttributeValueMemberN{Value: "42"},
		},
		{
			"id":     &ddbtypes.AttributeValueMemberS{Value: "2"},
			"region": &ddbtypes.AttributeValueMemberS{Value: "amer"},
			"amount": &ddbtypes.AttributeValueMemberN{Value: "84"},
		},
	} {
		_, err = dynamoDBClient.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: aws.String("glue-business-entities"),
			Item:      item,
		})
		require.NoError(t, err)
	}
	_, err = client.CreateConnection(ctx, &glue.CreateConnectionInput{
		ConnectionInput: &gluetypes.ConnectionInput{
			Name:                 aws.String("glue-dynamodb-connection"),
			ConnectionType:       gluetypes.ConnectionTypeDynamodb,
			ConnectionProperties: map[string]string{},
		},
	})
	require.NoError(t, err)

	_, err = client.CreateDatabase(ctx, &glue.CreateDatabaseInput{
		DatabaseInput: &gluetypes.DatabaseInput{Name: aws.String("business_entities")},
	})
	require.NoError(t, err)
	_, err = client.CreateTable(ctx, &glue.CreateTableInput{
		DatabaseName: aws.String("business_entities"),
		TableInput: &gluetypes.TableInput{
			Name: aws.String("sales"),
			StorageDescriptor: &gluetypes.StorageDescriptor{
				Columns: []gluetypes.Column{
					{Name: aws.String("id"), Type: aws.String("string")},
					{Name: aws.String("region"), Type: aws.String("string")},
					{Name: aws.String("amount"), Type: aws.String("int")},
				},
			},
		},
	})
	require.NoError(t, err)

	entities, err := client.ListEntities(ctx, &glue.ListEntitiesInput{})
	require.NoError(t, err)
	require.NotEmpty(t, entities.Entities)
	children, err := client.ListEntities(ctx, &glue.ListEntitiesInput{
		ConnectionName: aws.String("glue-dynamodb-connection"),
	})
	require.NoError(t, err)
	var foundEntity bool
	for _, entity := range children.Entities {
		if aws.ToString(entity.EntityName) == "glue-business-entities" {
			foundEntity = true
			break
		}
	}
	require.True(t, foundEntity, "connection entities should include the created DynamoDB table")

	description, err := client.DescribeEntity(ctx, &glue.DescribeEntityInput{
		ConnectionName: aws.String("glue-dynamodb-connection"),
		EntityName:     aws.String("glue-business-entities"),
	})
	require.NoError(t, err)
	require.Len(t, description.Fields, 1)
	assert.Equal(t, gluetypes.FieldDataTypeString, description.Fields[0].FieldType)

	records, err := client.GetEntityRecords(ctx, &glue.GetEntityRecordsInput{
		ConnectionName:  aws.String("glue-dynamodb-connection"),
		EntityName:      aws.String("glue-business-entities"),
		Limit:           aws.Int64(10),
		FilterPredicate: aws.String("region='emea'"),
		SelectedFields:  []string{"id", "amount"},
	})
	require.NoError(t, err)
	require.Len(t, records.Records, 1)
	var record map[string]any
	require.NoError(t, records.Records[0].UnmarshalSmithyDocument(&record))
	assert.Equal(t, "1", record["id"])
	assert.Equal(t, "42", fmt.Sprint(record["amount"]))

	_, err = client.CreateDataQualityRuleset(ctx, &glue.CreateDataQualityRulesetInput{
		Name:    aws.String("business-context-rules"),
		Ruleset: aws.String("Rules = [ColumnExists \"id\"]"),
	})
	require.NoError(t, err)
	run, err := client.StartDataQualityRulesetEvaluationRun(ctx, &glue.StartDataQualityRulesetEvaluationRunInput{
		DataSource: &gluetypes.DataSource{
			GlueTable: &gluetypes.GlueTable{
				DatabaseName: aws.String("business_entities"),
				TableName:    aws.String("sales"),
			},
		},
		Role:         aws.String("arn:aws:iam::000000000000:role/glue-data-quality"),
		RulesetNames: []string{"business-context-rules"},
	})
	require.NoError(t, err)
	batch, err := client.BatchGetDataQualityRulesetEvaluationRun(ctx, &glue.BatchGetDataQualityRulesetEvaluationRunInput{
		RunIds: []string{aws.ToString(run.RunId), "missing-run"},
	})
	require.NoError(t, err)
	require.Len(t, batch.Runs, 1)
	assert.Equal(t, aws.ToString(run.RunId), aws.ToString(batch.Runs[0].RunId))
	assert.Equal(t, []string{"missing-run"}, batch.RunsNotFound)
}

// TestGlueIterableFormsOfATableAsset_SDK exercises the iterable-form surface
// through the only route that reaches it.
//
// No operation in the AWS Glue business-context API creates an iterable form:
// PutAsset, PutAssetType, PutFormType and PutAttachment take none, and
// IterableFormMap appears in the vendored model only on GetAssetOutput, where
// it is "The iterable forms available on the asset, keyed by form name (for
// example, columns)". An iterable form is the catalog object's own repeating
// structure — ListIterableForms is documented as "Lists the items in an
// iterable form on an asset in Glue Data Catalog. For example, lists the
// columns of a table asset" — so an asset that names a Data Catalog table
// carries that table's columns, and PutAttachment and AssociateGlossaryTerms
// annotate the items that are already there.
func TestGlueIterableFormsOfATableAsset_SDK(t *testing.T) {
	client := glueClient()

	_, err := client.CreateDatabase(ctx, &glue.CreateDatabaseInput{
		DatabaseInput: &gluetypes.DatabaseInput{Name: aws.String("iterable_forms")},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteDatabase(ctx, &glue.DeleteDatabaseInput{Name: aws.String("iterable_forms")})
	})
	_, err = client.CreateTable(ctx, &glue.CreateTableInput{
		DatabaseName: aws.String("iterable_forms"),
		TableInput: &gluetypes.TableInput{
			Name: aws.String("orders"),
			StorageDescriptor: &gluetypes.StorageDescriptor{
				Columns: []gluetypes.Column{
					{Name: aws.String("order_id"), Type: aws.String("string"), Comment: aws.String("the order")},
					{Name: aws.String("amount"), Type: aws.String("double")},
				},
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteTable(ctx, &glue.DeleteTableInput{
			DatabaseName: aws.String("iterable_forms"), Name: aws.String("orders")})
	})

	formType, err := client.PutFormType(ctx, &glue.PutFormTypeInput{
		Name:   aws.String("ColumnGovernance"),
		Schema: aws.String("structure ColumnGovernance { classification: String }"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteFormType(ctx, &glue.DeleteFormTypeInput{Identifier: formType.Id})
	})
	assetType, err := client.PutAssetType(ctx, &glue.PutAssetTypeInput{
		Name: aws.String("GovernedTable"),
		Forms: map[string]gluetypes.AssetTypeFormReference{
			"governance": {FormTypeIdentifier: formType.Id},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteAssetType(ctx, &glue.DeleteAssetTypeInput{Identifier: assetType.Id})
	})

	// A table asset is identified by the table's own ARN, which is how AWS names
	// a Data Catalog table.
	tableARN := "arn:aws:glue:us-east-1:123456789012:table/iterable_forms/orders"
	_, err = client.PutAsset(ctx, &glue.PutAssetInput{
		AssetTypeId: assetType.Id,
		Identifier:  aws.String(tableARN),
		Name:        aws.String("orders"),
		Forms: map[string]gluetypes.AssetFormEntry{
			"governance": {FormTypeId: formType.Id, Content: aws.String(`{"classification":"internal"}`)},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteAsset(ctx, &glue.DeleteAssetInput{Identifier: aws.String(tableARN)})
	})

	gotAsset, err := client.GetAsset(ctx, &glue.GetAssetInput{Identifier: aws.String(tableARN)})
	require.NoError(t, err)
	require.Contains(t, gotAsset.IterableForms, "columns",
		"a table asset must advertise the columns iterable form")
	assert.Equal(t, "columns", aws.ToString(gotAsset.IterableForms["columns"].FormTypeId))

	listed, err := client.ListIterableForms(ctx, &glue.ListIterableFormsInput{
		AssetIdentifier:  aws.String(tableARN),
		IterableFormName: aws.String("columns"),
	})
	require.NoError(t, err)
	require.Len(t, listed.Items, 2, "the form must list the table's two columns")
	names := []string{aws.ToString(listed.Items[0].ItemName), aws.ToString(listed.Items[1].ItemName)}
	assert.Equal(t, []string{"amount", "order_id"}, names)
	for _, item := range listed.Items {
		if aws.ToString(item.ItemName) == "order_id" {
			assert.Equal(t, "the order", aws.ToString(item.Description),
				"a column's description is its catalog comment")
		}
	}

	batch, err := client.BatchGetIterableForms(ctx, &glue.BatchGetIterableFormsInput{
		AssetIdentifier:  aws.String(tableARN),
		IterableFormName: aws.String("columns"),
		ItemIdentifiers:  []string{"order_id", "no_such_column"},
	})
	require.NoError(t, err)
	require.Len(t, batch.Items, 1)
	assert.Equal(t, "order_id", aws.ToString(batch.Items[0].ItemId))
	require.Contains(t, batch.Items[0].Forms, "columns")
	assert.JSONEq(t, `{"Name":"order_id","Type":"string","Comment":"the order"}`,
		aws.ToString(batch.Items[0].Forms["columns"].Content),
		"the item's form is the column the catalog holds")
	require.Len(t, batch.Errors, 1)
	assert.Equal(t, "no_such_column", aws.ToString(batch.Errors[0].ItemIdentifier))

	// An item that exists can be annotated, which is what the model's
	// IterableFormName + ItemIdentifier members on PutAttachment and
	// AssociateGlossaryTerms are for.
	attachment, err := client.PutAttachment(ctx, &glue.PutAttachmentInput{
		AssetIdentifier:  aws.String(tableARN),
		IterableFormName: aws.String("columns"),
		ItemIdentifier:   aws.String("order_id"),
		AttachmentName:   aws.String("governance"),
		FormTypeId:       formType.Id,
		Content:          aws.String(`{"classification":"pii"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, "columns", aws.ToString(attachment.IterableFormName))
	assert.Equal(t, "order_id", aws.ToString(attachment.ItemIdentifier))

	glossary, err := client.CreateGlossary(ctx, &glue.CreateGlossaryInput{
		Name:        aws.String("Column Terms"),
		ClientToken: aws.String("iterable-forms-glossary"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteGlossary(ctx, &glue.DeleteGlossaryInput{Identifier: glossary.Id})
	})
	term, err := client.CreateGlossaryTerm(ctx, &glue.CreateGlossaryTermInput{
		GlossaryIdentifier: glossary.Id,
		Name:               aws.String("Order Identifier"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteGlossaryTerm(ctx, &glue.DeleteGlossaryTermInput{Identifier: term.Id})
	})
	associated, err := client.AssociateGlossaryTerms(ctx, &glue.AssociateGlossaryTermsInput{
		AssetIdentifier:         aws.String(tableARN),
		IterableFormName:        aws.String("columns"),
		ItemIdentifier:          aws.String("order_id"),
		GlossaryTermIdentifiers: []string{aws.ToString(term.Id)},
	})
	require.NoError(t, err)
	assert.Equal(t, "columns", aws.ToString(associated.IterableFormName))
	assert.Equal(t, []string{aws.ToString(term.Id)}, associated.GlossaryTerms)

	annotated, err := client.BatchGetIterableForms(ctx, &glue.BatchGetIterableFormsInput{
		AssetIdentifier:  aws.String(tableARN),
		IterableFormName: aws.String("columns"),
		ItemIdentifiers:  []string{"order_id"},
	})
	require.NoError(t, err)
	require.Len(t, annotated.Items, 1)
	require.Contains(t, annotated.Items[0].Attachments, "governance")
	assert.Equal(t, []string{aws.ToString(term.Id)}, annotated.Items[0].GlossaryTerms)

	// The annotations belong to the item, not to the asset.
	wholeAsset, err := client.GetAsset(ctx, &glue.GetAssetInput{Identifier: aws.String(tableARN)})
	require.NoError(t, err)
	assert.NotContains(t, wholeAsset.Attachments, "governance")
	assert.Empty(t, wholeAsset.GlossaryTerms)

	// A form name the asset does not carry is still not found.
	_, err = client.ListIterableForms(ctx, &glue.ListIterableFormsInput{
		AssetIdentifier:  aws.String(tableARN),
		IterableFormName: aws.String("partitions"),
	})
	assert.Equal(t, "EntityNotFoundException", errCode(t, err))
}
