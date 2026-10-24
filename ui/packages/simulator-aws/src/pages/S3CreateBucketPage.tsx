import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Container from "@cloudscape-design/components/container";
import Header from "@cloudscape-design/components/header";
import Form from "@cloudscape-design/components/form";
import FormField from "@cloudscape-design/components/form-field";
import Input from "@cloudscape-design/components/input";
import RadioGroup from "@cloudscape-design/components/radio-group";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Button from "@cloudscape-design/components/button";
import Box from "@cloudscape-design/components/box";
import { AwsPageHeader, KeyValueEditor, rowsAreValid, rowsToTags, errorText, useNotify, type KeyValueRow } from "../console/index.js";
import { currentRegion } from "../console/federation.js";
import { regionName } from "../console/regions.js";
import { createS3Bucket, putS3BucketTagging, putS3BucketVersioning } from "../api.js";
import { isValidS3BucketName } from "./S3BucketsPage.js";

/**
 * The real console's Create bucket page: General configuration, Bucket
 * Versioning, and Tags. Create issues CreateBucket, then PutBucketVersioning
 * and PutBucketTagging when the operator asked for them.
 */
export function S3CreateBucketPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const notify = useNotify();
  const region = currentRegion();
  const [name, setName] = useState("");
  const [versioning, setVersioning] = useState<"Disabled" | "Enabled">("Disabled");
  const [tags, setTags] = useState<KeyValueRow[]>([]);
  const [touched, setTouched] = useState(false);
  const trimmed = name.trim();
  const valid = isValidS3BucketName(trimmed);
  const create = useMutation({
    mutationFn: async () => {
      await createS3Bucket(trimmed);
      if (versioning === "Enabled") await putS3BucketVersioning(trimmed, "Enabled");
      const tagSet = rowsToTags(tags);
      if (Object.keys(tagSet).length > 0) await putS3BucketTagging(trimmed, tagSet);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["s3-buckets"] });
      notify("success", `Successfully created bucket "${trimmed}"`);
      navigate("/ui/s3");
    },
  });
  return (
    <ContentLayout header={<AwsPageHeader title="Create bucket" description="Buckets are containers for data stored in S3." />}>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          setTouched(true);
          if (valid && rowsAreValid(tags)) create.mutate();
        }}
      >
        <Form
          actions={
            <SpaceBetween direction="horizontal" size="xs">
              <Button formAction="none" variant="link" onClick={() => navigate("/ui/s3")}>
                Cancel
              </Button>
              <Button variant="primary" loading={create.isPending} nativeButtonAttributes={{ "data-testid": "s3-create-bucket-submit" }}>
                Create bucket
              </Button>
            </SpaceBetween>
          }
          errorText={create.isError ? errorText(create.error) : undefined}
        >
          <SpaceBetween size="l">
            <Container header={<Header variant="h2">General configuration</Header>}>
              <SpaceBetween size="l">
                <FormField label="AWS Region">
                  <Box>
                    {regionName(region)} {region}
                  </Box>
                </FormField>
                <FormField
                  label="Bucket name"
                  description="Bucket names must be 3 to 63 characters and unique within the global namespace. Bucket names must also begin and end with a letter or number. Valid characters are a-z, 0-9, periods (.), and hyphens (-)."
                  errorText={touched && !valid ? "Bucket name must be 3 to 63 characters of lowercase letters, numbers, periods, and hyphens, beginning and ending with a letter or number." : undefined}
                >
                  <Input
                    value={name}
                    placeholder="myawsbucket"
                    onChange={(event) => setName(event.detail.value)}
                    nativeInputAttributes={{ "data-testid": "s3-bucket-name-input" }}
                  />
                </FormField>
              </SpaceBetween>
            </Container>
            <Container
              header={
                <Header variant="h2" description="Versioning is a means of keeping multiple variants of an object in the same bucket.">
                  Bucket Versioning
                </Header>
              }
            >
              <FormField label="Bucket Versioning">
                <RadioGroup
                  value={versioning}
                  onChange={(event) => setVersioning(event.detail.value as "Disabled" | "Enabled")}
                  items={[
                    { value: "Disabled", label: "Disable" },
                    { value: "Enabled", label: "Enable" },
                  ]}
                />
              </FormField>
            </Container>
            <Container
              header={
                <Header variant="h2" description="You can use bucket tags to track storage costs and organize buckets.">
                  Tags - optional
                </Header>
              }
            >
              <KeyValueEditor
                rows={tags}
                onChange={setTags}
                keyLabel="Key"
                valueLabel="Value"
                addLabel="Add new tag"
                emptyText="No tags associated with the resource."
                testIdPrefix="s3-create-bucket-tag"
              />
            </Container>
          </SpaceBetween>
        </Form>
      </form>
    </ContentLayout>
  );
}
