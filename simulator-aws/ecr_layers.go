package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// ECR repository policy + image-layer data plane.
//
// Repository policy backs the Terraform `aws_ecr_repository_policy` resource.
// The layer ops are the real (awsJson) image-transfer pipeline used by the
// SDK/CLI: InitiateLayerUpload → UploadLayerPart → CompleteLayerUpload stores
// a content-addressed blob; GetDownloadUrlForLayer serves it back; and
// BatchCheckLayerAvailability now reports real availability from this store
// instead of unconditional AVAILABLE.

type ecrLayer struct {
	RepositoryName string `json:"repositoryName"`
	Digest         string `json:"digest"`
	Data           []byte `json:"data"`
}

type ecrLayerUpload struct {
	UploadId       string `json:"uploadId"`
	RepositoryName string `json:"repositoryName"`
	Buffer         []byte `json:"buffer"`
}

var (
	ecrRepoPolicies sim.Store[string]         // repositoryName -> policy JSON
	ecrLayers       sim.Store[ecrLayer]       // repositoryName@digest -> blob
	ecrLayerUploads sim.Store[ecrLayerUpload] // uploadId -> in-progress session
)

func registerECRLayers(r *sim.AWSRouter, srv *sim.Server) {
	ecrRepoPolicies = sim.MakeStore[string](srv.DB(), "ecr_repo_policies")
	ecrLayers = sim.MakeStore[ecrLayer](srv.DB(), "ecr_layers")
	ecrLayerUploads = sim.MakeStore[ecrLayerUpload](srv.DB(), "ecr_layer_uploads")

	r.Register("AmazonEC2ContainerRegistry_V20150921.SetRepositoryPolicy", handleECRSetRepositoryPolicy)
	r.Register("AmazonEC2ContainerRegistry_V20150921.GetRepositoryPolicy", handleECRGetRepositoryPolicy)
	r.Register("AmazonEC2ContainerRegistry_V20150921.DeleteRepositoryPolicy", handleECRDeleteRepositoryPolicy)
	r.Register("AmazonEC2ContainerRegistry_V20150921.InitiateLayerUpload", handleECRInitiateLayerUpload)
	r.Register("AmazonEC2ContainerRegistry_V20150921.UploadLayerPart", handleECRUploadLayerPart)
	r.Register("AmazonEC2ContainerRegistry_V20150921.CompleteLayerUpload", handleECRCompleteLayerUpload)
	r.Register("AmazonEC2ContainerRegistry_V20150921.GetDownloadUrlForLayer", handleECRGetDownloadUrlForLayer)
}

func ecrLayerKey(repo, digest string) string { return repo + "@" + digest }

func ecrRepoExists(name string) bool {
	_, ok := ecrRepositories.Get(name)
	return ok
}

func handleECRSetRepositoryPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		PolicyText     string `json:"policyText"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecrRepoExists(req.RepositoryName) {
		sim.AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
			"The repository with name '%s' does not exist in the registry with id '%s'", req.RepositoryName, ecrRegistryId())
		return
	}
	ecrRepoPolicies.Put(req.RepositoryName, req.PolicyText)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":     ecrRegistryId(),
		"repositoryName": req.RepositoryName,
		"policyText":     req.PolicyText,
	})
}

func handleECRGetRepositoryPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecrRepoExists(req.RepositoryName) {
		sim.AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
			"The repository with name '%s' does not exist in the registry with id '%s'", req.RepositoryName, ecrRegistryId())
		return
	}
	policy, ok := ecrRepoPolicies.Get(req.RepositoryName)
	if !ok {
		sim.AWSErrorf(w, "RepositoryPolicyNotFoundException", http.StatusBadRequest,
			"Repository policy does not exist for the repository with name '%s'", req.RepositoryName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":     ecrRegistryId(),
		"repositoryName": req.RepositoryName,
		"policyText":     policy,
	})
}

func handleECRDeleteRepositoryPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	policy, ok := ecrRepoPolicies.Get(req.RepositoryName)
	if !ok {
		sim.AWSErrorf(w, "RepositoryPolicyNotFoundException", http.StatusBadRequest,
			"Repository policy does not exist for the repository with name '%s'", req.RepositoryName)
		return
	}
	ecrRepoPolicies.Delete(req.RepositoryName)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":     ecrRegistryId(),
		"repositoryName": req.RepositoryName,
		"policyText":     policy,
	})
}

func handleECRInitiateLayerUpload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecrRepoExists(req.RepositoryName) {
		sim.AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
			"The repository with name '%s' does not exist in the registry with id '%s'", req.RepositoryName, ecrRegistryId())
		return
	}
	uploadId := generateUUID()
	ecrLayerUploads.Put(uploadId, ecrLayerUpload{UploadId: uploadId, RepositoryName: req.RepositoryName})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"uploadId": uploadId,
		"partSize": 10485760, // 10 MiB recommended part size, as real ECR returns
	})
}

func handleECRUploadLayerPart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		UploadId       string `json:"uploadId"`
		PartFirstByte  int64  `json:"partFirstByte"`
		PartLastByte   int64  `json:"partLastByte"`
		LayerPartBlob  []byte `json:"layerPartBlob"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	session, ok := ecrLayerUploads.Get(req.UploadId)
	if !ok {
		sim.AWSErrorf(w, "UploadNotFoundException", http.StatusBadRequest,
			"Upload with id '%s' does not exist for the repository with name '%s'", req.UploadId, req.RepositoryName)
		return
	}
	session.Buffer = append(session.Buffer, req.LayerPartBlob...)
	ecrLayerUploads.Put(req.UploadId, session)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":       ecrRegistryId(),
		"repositoryName":   req.RepositoryName,
		"uploadId":         req.UploadId,
		"lastByteReceived": int64(len(session.Buffer) - 1),
	})
}

func handleECRCompleteLayerUpload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string   `json:"repositoryName"`
		UploadId       string   `json:"uploadId"`
		LayerDigests   []string `json:"layerDigests"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	session, ok := ecrLayerUploads.Get(req.UploadId)
	if !ok {
		sim.AWSErrorf(w, "UploadNotFoundException", http.StatusBadRequest,
			"Upload with id '%s' does not exist for the repository with name '%s'", req.UploadId, req.RepositoryName)
		return
	}
	if len(req.LayerDigests) == 0 {
		sim.AWSError(w, "InvalidParameterException", "layerDigests is required", http.StatusBadRequest)
		return
	}
	declared := req.LayerDigests[0]
	computed := fmt.Sprintf("sha256:%x", sha256.Sum256(session.Buffer))
	if declared != computed {
		sim.AWSErrorf(w, "InvalidLayerException", http.StatusBadRequest,
			"Layer digest '%s' does not match the computed digest '%s'", declared, computed)
		return
	}
	ecrLayers.Put(ecrLayerKey(req.RepositoryName, computed), ecrLayer{
		RepositoryName: req.RepositoryName,
		Digest:         computed,
		Data:           session.Buffer,
	})
	ecrLayerUploads.Delete(req.UploadId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":     ecrRegistryId(),
		"repositoryName": req.RepositoryName,
		"uploadId":       req.UploadId,
		"layerDigest":    computed,
	})
}

func handleECRGetDownloadUrlForLayer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		LayerDigest    string `json:"layerDigest"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ecrLayers.Get(ecrLayerKey(req.RepositoryName, req.LayerDigest)); !ok {
		sim.AWSErrorf(w, "LayerInaccessibleException", http.StatusBadRequest,
			"Layer '%s' is not available in the repository with name '%s'", req.LayerDigest, req.RepositoryName)
		return
	}
	// Real ECR returns a presigned S3 URL. The sim holds layer blobs in its own
	// store, so it returns a reference to its registry endpoint for the layer;
	// the URL is honest only insofar as the layer actually exists (checked
	// above) — it is not an S3 presign.
	url := fmt.Sprintf("http://%s.dkr.ecr.%s.amazonaws.com/v2/%s/blobs/%s",
		ecrRegistryId(), awsRegion(), req.RepositoryName, req.LayerDigest)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"layerDigest": req.LayerDigest,
		"downloadUrl": url,
	})
}
