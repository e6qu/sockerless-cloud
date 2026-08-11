import { describe, expect, it } from "vitest";
import { parseBlobListXML, parseContainerListXML } from "../api.js";

// The real Azure Storage `ListContainers` / `ListBlobs` responses are XML
// (EnumerationResults), not JSON — these fixtures are the literal shape
// simulator-azure/blob.go's handleListContainers/handleListBlobs marshal,
// the same shape a real storage account returns.
const CONTAINERS_XML = `<?xml version="1.0" encoding="utf-8"?>
<EnumerationResults ServiceEndpoint="https://acct.blob.localhost:4568/">
  <Containers>
    <Container>
      <Name>logs</Name>
      <Properties>
        <Last-Modified>Wed, 01 Jan 2026 00:00:00 GMT</Last-Modified>
        <Etag>"0x1"</Etag>
      </Properties>
    </Container>
    <Container>
      <Name>artifacts</Name>
      <Properties>
        <Last-Modified>Wed, 01 Jan 2026 00:00:00 GMT</Last-Modified>
        <Etag>"0x2"</Etag>
      </Properties>
    </Container>
  </Containers>
  <NextMarker/>
</EnumerationResults>`;

const BLOBS_XML = `<?xml version="1.0" encoding="utf-8"?>
<EnumerationResults ServiceEndpoint="https://acct.blob.localhost:4568/" ContainerName="logs">
  <Blobs>
    <Blob>
      <Name>run-1.log</Name>
      <Properties>
        <Content-Length>1234</Content-Length>
        <Etag>"0x3"</Etag>
        <Last-Modified>Wed, 01 Jan 2026 00:00:00 GMT</Last-Modified>
      </Properties>
    </Blob>
  </Blobs>
  <NextMarker/>
</EnumerationResults>`;

describe("parseContainerListXML", () => {
  it("reads container names from the real ListContainers XML shape", () => {
    expect(parseContainerListXML(CONTAINERS_XML)).toEqual([{ name: "logs" }, { name: "artifacts" }]);
  });

  it("returns an empty array for an empty result", () => {
    expect(
      parseContainerListXML(
        `<?xml version="1.0"?><EnumerationResults ServiceEndpoint="https://x/"><Containers/><NextMarker/></EnumerationResults>`,
      ),
    ).toEqual([]);
  });
});

describe("parseBlobListXML", () => {
  it("reads blob name, content length, and last-modified from the real ListBlobs XML shape", () => {
    expect(parseBlobListXML(BLOBS_XML)).toEqual([
      { name: "run-1.log", contentLength: 1234, lastModified: "Wed, 01 Jan 2026 00:00:00 GMT" },
    ]);
  });

  it("returns an empty array for an empty result", () => {
    expect(
      parseBlobListXML(
        `<?xml version="1.0"?><EnumerationResults ServiceEndpoint="https://x/" ContainerName="c"><Blobs/><NextMarker/></EnumerationResults>`,
      ),
    ).toEqual([]);
  });
});
