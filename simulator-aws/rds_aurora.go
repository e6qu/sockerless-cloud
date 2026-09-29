package main

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

func rdsIsAurora(engine string) bool {
	return strings.EqualFold(engine, "aurora-postgresql") || strings.EqualFold(engine, "aurora-mysql")
}

func rdsClusterVolume(clusterID string) string { return "sockerless-rds-cluster-" + clusterID }

// rdsAuroraDataPlane is an Aurora cluster's engine over its cluster volume.
// Every Aurora instance reads the one cluster volume, so the writer and
// reader endpoints and each member's instance endpoint relay to the one
// engine while an instance stands behind them.
type rdsAuroraDataPlane struct {
	clusterID     string
	engine        *dbengine.Instance
	engineAddress string
	writer        net.Listener
	reader        net.Listener
}

var (
	rdsAuroraDataPlanes        sync.Map
	rdsAuroraInstanceEndpoints sync.Map
)

func rdsLoadAuroraDataPlane(clusterID string) (*rdsAuroraDataPlane, bool) {
	value, ok := rdsAuroraDataPlanes.Load(clusterID)
	if !ok {
		return nil, false
	}
	plane, ok := value.(*rdsAuroraDataPlane)
	return plane, ok
}

// rdsInstallAuroraDataPlane binds an Aurora cluster's writer and reader
// endpoints and the engine behind them, sealing masterPassword into the
// cluster record the first time.
func rdsInstallAuroraDataPlane(cluster *RDSCluster, masterPassword string) error {
	if !rdsIsAurora(cluster.Engine) {
		return nil
	}
	engine, _ := rdsEngine(cluster.Engine)
	if len(cluster.MasterUserSecret) == 0 {
		if masterPassword == "" {
			return fmt.Errorf("MasterUserPassword is required for the %s data plane", cluster.Engine)
		}
		sealed, err := rdsSealMasterPassword(masterPassword)
		if err != nil {
			return err
		}
		cluster.MasterUserSecret = sealed
	}
	id := cluster.DBClusterIdentifier
	engineListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("allocate Aurora engine address: %w", err)
	}
	writer, err := rdsListenForEndpoint(cluster.Endpoint, cluster.Port, id+".cluster")
	if err != nil {
		_ = engineListener.Close()
		return fmt.Errorf("allocate Aurora cluster endpoint: %w", err)
	}
	reader, err := rdsListenForEndpoint(cluster.ReaderEndpoint, cluster.Port, id+".cluster-ro")
	if err != nil {
		_ = engineListener.Close()
		_ = writer.Close()
		return fmt.Errorf("allocate Aurora reader endpoint: %w", err)
	}
	writerIP, writerErr := rdsListenerIP(writer)
	readerIP, readerErr := rdsListenerIP(reader)
	if writerErr != nil || readerErr != nil {
		_ = engineListener.Close()
		_ = writer.Close()
		_ = reader.Close()
		return fmt.Errorf("allocate Aurora endpoints: %w", errors.Join(writerErr, readerErr))
	}
	cluster.Endpoint, cluster.ReaderEndpoint = writerIP, readerIP
	plane := &rdsAuroraDataPlane{
		clusterID:     id,
		engineAddress: engineListener.Addr().String(),
		writer:        writer,
		reader:        reader,
	}
	plane.engine = &dbengine.Instance{
		Name:         "Amazon Aurora " + id,
		Engine:       engine,
		Volume:       rdsClusterVolume(id),
		Labels:       map[string]string{"sockerless-rds-cluster": id},
		Sandbox:      SandboxFargate,
		Platform:     dbengine.FixedPlatform("linux/amd64"),
		Environment:  plane.environment,
		Certificate:  rdsServerCertificate,
		Authenticate: plane.authenticate,
		BackendLogin: plane.backendLogin,
	}
	rdsAuroraDataPlanes.Store(id, plane)
	plane.engine.Serve(engineListener)
	rdsServeRelay(writer, plane.writerTarget)
	rdsServeRelay(reader, plane.readerTarget)
	return nil
}

func rdsListenerIP(listener net.Listener) (string, error) {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return "", fmt.Errorf("RDS endpoint listener returned address type %T", listener.Addr())
	}
	return address.IP.String(), nil
}

func (plane *rdsAuroraDataPlane) masterPassword() (RDSCluster, string, error) {
	cluster, ok := rdsClusters.Get(plane.clusterID)
	if !ok {
		return RDSCluster{}, "", fmt.Errorf("DB cluster %s no longer exists", plane.clusterID)
	}
	_, password, ok := kmsDecryptBytes(cluster.MasterUserSecret)
	if !ok {
		return RDSCluster{}, "", fmt.Errorf("decrypt the Amazon Aurora master-user credential")
	}
	return cluster, string(password), nil
}

func (plane *rdsAuroraDataPlane) environment() (map[string]string, error) {
	cluster, password, err := plane.masterPassword()
	if err != nil {
		return nil, err
	}
	database := cluster.DatabaseName
	if database == "" {
		database = cluster.MasterUsername
	}
	return rdsEngineEnvironment(plane.engine.Engine, cluster.MasterUsername, password, database), nil
}

func (plane *rdsAuroraDataPlane) authenticate(user, password string, secure bool) bool {
	cluster, masterPassword, err := plane.masterPassword()
	if err != nil {
		return false
	}
	if user == cluster.MasterUsername && subtle.ConstantTimeCompare([]byte(password), []byte(masterPassword)) == 1 {
		return true
	}
	return secure && cluster.EnableIAMDatabaseAuthentication &&
		rdsValidateIAMAuthToken(rdsAuroraEndpoints(cluster), cluster.DbClusterResourceId, user, password)
}

func (plane *rdsAuroraDataPlane) backendLogin(string, string) (string, string, error) {
	cluster, password, err := plane.masterPassword()
	return cluster.MasterUsername, password, err
}

// rdsAuroraEndpoints lists every endpoint an IAM authentication token for the
// cluster may be signed for.
func rdsAuroraEndpoints(cluster RDSCluster) []string {
	port := strconv.Itoa(cluster.Port)
	endpoints := []string{net.JoinHostPort(cluster.Endpoint, port), net.JoinHostPort(cluster.ReaderEndpoint, port)}
	for _, member := range rdsClusterMembers(cluster.DBClusterIdentifier) {
		endpoints = append(endpoints, net.JoinHostPort(member.Endpoint, strconv.Itoa(member.Port)))
	}
	return endpoints
}

// writerTarget is the engine while the cluster's writer instance is
// available. An Aurora Serverless v1 cluster has no instances: its capacity
// serves the cluster endpoint itself.
func (plane *rdsAuroraDataPlane) writerTarget() (string, bool) {
	cluster, ok := rdsClusters.Get(plane.clusterID)
	if !ok || cluster.Status != "available" {
		return "", false
	}
	if cluster.EngineMode == "serverless" {
		return plane.engineAddress, true
	}
	members := rdsClusterMembers(plane.clusterID)
	if len(members) == 0 || members[0].DBInstanceStatus != "available" {
		return "", false
	}
	return plane.engineAddress, true
}

// readerTarget is the engine while any instance is available: the reader
// endpoint connects to the writer when the cluster has no Aurora Replica.
func (plane *rdsAuroraDataPlane) readerTarget() (string, bool) {
	cluster, ok := rdsClusters.Get(plane.clusterID)
	if !ok || cluster.Status != "available" {
		return "", false
	}
	for _, member := range rdsClusterMembers(plane.clusterID) {
		if member.DBInstanceStatus == "available" {
			return plane.engineAddress, true
		}
	}
	return "", false
}

// rdsInstallAuroraInstanceEndpoint binds an Aurora member's instance endpoint
// in front of its cluster's engine.
func rdsInstallAuroraInstanceEndpoint(instance *RDSInstance) error {
	if _, ok := rdsLoadAuroraDataPlane(instance.DBClusterIdentifier); !ok {
		return fmt.Errorf("DB cluster %s has no cluster volume to serve DB instance %s", instance.DBClusterIdentifier, instance.DBInstanceIdentifier)
	}
	listener, err := rdsListenForEndpoint(instance.Endpoint, instance.Port, instance.DBInstanceIdentifier)
	if err != nil {
		return fmt.Errorf("allocate RDS endpoint: %w", err)
	}
	endpointIP, err := rdsListenerIP(listener)
	if err != nil {
		_ = listener.Close()
		return err
	}
	instance.Endpoint = endpointIP
	id := instance.DBInstanceIdentifier
	rdsAuroraInstanceEndpoints.Store(id, listener)
	rdsServeRelay(listener, func() (string, bool) {
		member, ok := rdsInstances.Get(id)
		if !ok || member.DBInstanceStatus != "available" {
			return "", false
		}
		plane, ok := rdsLoadAuroraDataPlane(member.DBClusterIdentifier)
		if !ok {
			return "", false
		}
		return plane.engineAddress, true
	})
	return nil
}

func rdsCloseAuroraInstanceEndpoint(instanceID string) {
	if value, ok := rdsAuroraInstanceEndpoints.LoadAndDelete(instanceID); ok {
		if listener, ok := value.(net.Listener); ok {
			_ = listener.Close()
		}
	}
}

// rdsStopAuroraDataPlane closes an Aurora cluster's endpoints and stops its
// engine and, when the cluster is being deleted, removes its cluster volume.
func rdsStopAuroraDataPlane(clusterID string, deleteVolume bool) error {
	release := rdsDataPlaneStops.Lock("cluster/" + clusterID)
	defer release()
	var stopErr error
	if value, ok := rdsAuroraDataPlanes.LoadAndDelete(clusterID); ok {
		if plane, ok := value.(*rdsAuroraDataPlane); ok {
			_ = plane.writer.Close()
			_ = plane.reader.Close()
			if err := plane.engine.Close(); err != nil {
				stopErr = fmt.Errorf("stop database engine: %w", err)
				log.Printf("Amazon Aurora %s: %v", clusterID, stopErr)
			}
		}
	}
	if deleteVolume && sim.VolumeExists(rdsClusterVolume(clusterID)) {
		if err := sim.RemoveVolume(rdsClusterVolume(clusterID)); err != nil {
			log.Printf("Amazon Aurora %s: remove cluster volume: %v", clusterID, err)
		}
	}
	return stopErr
}

// rdsRecoverAuroraDataPlanes reinstalls the data planes of the Aurora
// clusters a previous process served and adopts their engines.
func rdsRecoverAuroraDataPlanes() error {
	for _, cluster := range rdsClusters.List() {
		serving := cluster.Status == "available" || cluster.Status == "stopping"
		if !rdsIsAurora(cluster.Engine) || !serving || len(cluster.MasterUserSecret) == 0 {
			continue
		}
		if err := rdsInstallAuroraDataPlane(&cluster, ""); err != nil {
			return fmt.Errorf("restore DB cluster %s: %w", cluster.DBClusterIdentifier, err)
		}
		if plane, ok := rdsLoadAuroraDataPlane(cluster.DBClusterIdentifier); ok {
			if err := plane.engine.Adopt(); err != nil {
				return fmt.Errorf("restore DB cluster %s backend: %w", cluster.DBClusterIdentifier, err)
			}
		}
		rdsClusters.Put(cluster.DBClusterIdentifier, cluster)
	}
	return nil
}

// rdsServeRelay relays each client of listener to the address open names and
// closes a client open refuses, as an endpoint with no instance behind it
// does.
func rdsServeRelay(listener net.Listener, open func() (string, bool)) {
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer client.Close()
				address, ok := open()
				if !ok {
					return
				}
				backend, err := net.DialTimeout("tcp", address, 5*time.Second)
				if err != nil {
					log.Printf("Amazon RDS endpoint %s: dial engine: %v", listener.Addr(), err)
					return
				}
				defer backend.Close()
				rdsRelayConnections(client, backend)
			}()
		}
	}()
}

// rdsRelayConnections copies both directions until either side finishes,
// half-closing the other side's write direction so a client's EOF reaches the
// engine.
func rdsRelayConnections(left, right net.Conn) {
	done := make(chan struct{}, 2)
	copySide := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}
	go copySide(left, right)
	go copySide(right, left)
	<-done
}
