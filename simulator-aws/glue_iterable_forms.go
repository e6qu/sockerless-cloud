package main

import (
	"encoding/json"
	"strings"
)

// Iterable forms on an AWS Glue Data Catalog asset.
//
// Nothing in the business-context API creates an iterable form. The vendored
// AWS Glue model's asset operations are PutAssetType, PutAsset, GetAsset,
// UpdateAsset, DeleteAsset, SearchAssets, PutAttachment, DeleteAttachment,
// ListIterableForms and BatchGetIterableForms, plus the glossary operations —
// and no input shape carries an iterable form or one of its items.
// IterableFormMap appears in exactly one place, GetAssetOutput, where it is
// described as "The iterable forms available on the asset, keyed by form name
// (for example, columns)". PutAttachment, DeleteAttachment,
// AssociateGlossaryTerms and DisassociateGlossaryTerms each take an
// IterableFormName plus an ItemIdentifier to annotate an item that is already
// there.
//
// An iterable form is therefore the catalog object's own repeating structure,
// surfaced on the asset that describes it: "Lists the items in an iterable form
// on an asset in Glue Data Catalog. For example, lists the columns of a table
// asset" (ListIterableForms). An asset that names a Data Catalog table by the
// table's ARN carries that table's columns iterable form, one item per column
// of the table's storage descriptor. An asset that names anything else carries
// no iterable form, and the two readers answer EntityNotFoundException for a
// form name on it — which is what the service answers for a form an asset does
// not have.
//
// Item-level attachments and glossary terms are the parts callers do write, so
// they are stored on the asset and merged onto the derived items here.

// glueColumnsIterableFormName is the form name and form type id of the columns
// iterable form — the example AWS gives for both.
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

// glueAssetTableColumns returns the columns of the Data Catalog table an asset
// identifier names, and whether the identifier named a table at all. The
// identifier is the table's ARN — arn:aws:glue:<region>:<account>:table/<db>/<table>
// — which is how AWS names a catalog table and which the AssetId pattern
// (^[a-zA-Z0-9\-\:\/\.\_\*]+$) admits.
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

// glueStoredIterableItem returns the asset's stored annotations for one item of
// an iterable form, creating the form's entry when this is the first annotation
// on it. It reports false when the form or the item is not one the asset
// carries — an identifier may be the item's id or its name.
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

// glueStoreIterableItemAnnotations writes an item's attachments and glossary
// terms back onto the asset.
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
