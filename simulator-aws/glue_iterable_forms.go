package main

import (
	"encoding/json"
	"strings"
)

// Nothing in the AWS Glue business-context API creates an iterable form: no
// input shape carries one, and only GetAssetOutput's IterableFormMap reports
// them. An iterable form is the catalog object's own repeating structure, so an
// asset that names a Data Catalog table by ARN carries that table's columns form,
// one item per storage-descriptor column. Any other asset carries none, and the
// readers answer EntityNotFoundException, as the service does for a form an
// asset lacks. Callers write item-level attachments and glossary terms; store
// those on the asset and merge them onto the derived items.

// The columns form's name and form type id, as AWS's own example gives both.
const glueColumnsIterableFormName = "columns"

// glueAssetIterableForms returns the iterable forms an asset carries, keyed by
// form name, with each item's stored annotations merged in.
func glueAssetIterableForms(asset GlueBusinessAsset) map[string]GlueBusinessIterableForm {
	forms := map[string]GlueBusinessIterableForm{}
	columns, ok := glueAssetTableColumns(asset.Id)
	if !ok {
		return forms
	}
	stored := asset.IterableForms[glueColumnsIterableFormName]
	items := make(map[string]GlueBusinessIterableItem, len(columns))
	for _, column := range columns {
		name := glueString(column["Name"])
		if name == "" {
			continue
		}
		content, err := json.Marshal(column)
		if err != nil {
			continue
		}
		item := GlueBusinessIterableItem{
			ItemId:      name,
			ItemName:    name,
			Description: glueString(column["Comment"]),
			Forms: map[string]GlueBusinessAssetFormEntry{
				glueColumnsIterableFormName: {
					FormTypeId: glueColumnsIterableFormName,
					Content:    string(content),
				},
			},
		}
		if annotated, ok := stored.Items[name]; ok {
			item.Attachments = annotated.Attachments
			item.GlossaryTerms = annotated.GlossaryTerms
		}
		items[name] = item
	}
	forms[glueColumnsIterableFormName] = GlueBusinessIterableForm{
		FormTypeId: glueColumnsIterableFormName,
		Items:      items,
	}
	return forms
}

// glueAssetTableColumns returns the columns of the Data Catalog table an asset ID
// names by ARN (arn:aws:glue:<region>:<account>:table/<db>/<table>), and whether
// it named a table at all.
func glueAssetTableColumns(assetID string) ([]map[string]any, bool) {
	resourceType, resource := glueResourceFromARN(assetID)
	if !strings.HasPrefix(assetID, "arn:") || resourceType != "table" {
		return nil, false
	}
	database, name, found := strings.Cut(resource, "/")
	if !found || database == "" || name == "" {
		return nil, false
	}
	table, ok := glueTables.Get(glueTableKey(database, name))
	if !ok {
		return nil, false
	}
	raw, ok := table.StorageDescriptor["Columns"].([]any)
	if !ok {
		return nil, true
	}
	columns := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		column, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		columns = append(columns, column)
	}
	return columns, true
}

// glueStoredIterableItem returns the asset's stored annotations for one item,
// creating the form's entry on first use. It reports false when the asset
// carries no such form or item; itemIdentifier may be the item's id or name.
func glueStoredIterableItem(
	asset *GlueBusinessAsset, formName, itemIdentifier string,
) (GlueBusinessIterableItem, bool) {
	form, ok := glueAssetIterableForms(*asset)[formName]
	if !ok {
		return GlueBusinessIterableItem{}, false
	}
	item, ok := form.Items[itemIdentifier]
	if !ok {
		for _, candidate := range form.Items {
			if candidate.ItemName == itemIdentifier {
				item, ok = candidate, true
				break
			}
		}
	}
	if !ok {
		return GlueBusinessIterableItem{}, false
	}
	if asset.IterableForms == nil {
		asset.IterableForms = map[string]GlueBusinessIterableForm{}
	}
	stored, ok := asset.IterableForms[formName]
	if !ok {
		stored = GlueBusinessIterableForm{FormTypeId: form.FormTypeId}
	}
	if stored.Items == nil {
		stored.Items = map[string]GlueBusinessIterableItem{}
	}
	asset.IterableForms[formName] = stored
	return item, true
}

func glueStoreIterableItemAnnotations(
	asset *GlueBusinessAsset, formName string, item GlueBusinessIterableItem,
) {
	stored := asset.IterableForms[formName]
	if stored.Items == nil {
		stored.Items = map[string]GlueBusinessIterableItem{}
	}
	stored.Items[item.ItemId] = GlueBusinessIterableItem{
		ItemId:        item.ItemId,
		ItemName:      item.ItemName,
		Attachments:   item.Attachments,
		GlossaryTerms: item.GlossaryTerms,
	}
	asset.IterableForms[formName] = stored
}
