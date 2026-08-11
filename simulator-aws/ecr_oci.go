package main

import (
	"crypto/sha256"
	"fmt"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// ECR OCI Distribution data plane. The ECR control plane
// (CreateRepository / DescribeImages / layer ops) is awsJson; the actual
// `docker push` / `docker pull` go over the OCI Distribution `/v2/` API, which
// the AWS simulator previously didn't serve at all (`GET /v2/` → 404). This
// mounts the shared OCI data plane and registers each pushed manifest as an ECR
// image so the control plane sees it.
func registerECROCI(srv *sim.Server) {
	reg := &sim.OCIRegistry{
		Manifests: sim.MakeStore[sim.OCIManifest](srv.DB(), "ecr_oci_manifests"),
		Blobs:     sim.MakeStore[sim.OCIBlob](srv.DB(), "ecr_oci_blobs"),
		Uploads:   sim.MakeStore[sim.OCIUpload](srv.DB(), "ecr_oci_uploads"),
		OnManifestPut: func(repo, ref, contentType string, data []byte) {
			digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
			detail := ECRImageDetail{
				RegistryId:     ecrRegistryId(),
				RepositoryName: repo,
				ImageDigest:    digest,
				ImageTags:      []string{ref},
				ImageManifest:  string(data),
				PushedAt:       time.Now().Unix(),
			}
			ecrImages.Put(repo+":"+ref, detail)
			ecrImages.Put(repo+":"+digest, detail)
			ecrBumpImageGen()
		},
	}
	reg.Register(srv)
}
