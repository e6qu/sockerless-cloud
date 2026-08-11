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

	_, err = client.ListIterableForms(ctx, &glue.ListIterableFormsInput{
		AssetIdentifier:  aws.String("quarterly-sales"),
		IterableFormName: aws.String("columns"),
	})
	require.Error(t, err)
	_, err = client.BatchGetIterableForms(ctx, &glue.BatchGetIterableFormsInput{
		AssetIdentifier:  aws.String("quarterly-sales"),
		IterableFormName: aws.String("columns"),
		ItemIdentifiers:  []string{"amount"},
	})
	require.Error(t, err)

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
