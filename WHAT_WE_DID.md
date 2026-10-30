# WHAT WE DID

The decisions behind the shape of this repository and the lessons that stop
them being relearned. `CHANGELOG.md` dates each release, `BUGS.md` holds the
open bugs (resolved ones live in git history), `STATUS.md` is the snapshot and
`PLAN.md` the roadmap.

## Origin

The simulators came out of the sockerless monorepo as a fresh snapshot without
history. The per-cloud directories became `simulator-{aws,gcp,azure}` so
`go install` produces binaries with those names, and every module path moved
to `github.com/e6qu/sockerless-cloud/*`.

The simulators are a general-purpose reimplementation of slices of the clouds,
not a component of their first consumer. No image path, app setting or header
named after a consumer changes what a simulator does.

## The framework is one module

Each simulator once carried its own `shared/` copy of the framework, and the
copies drifted: fixes landed in one and never the others. The framework became
the `sim` module, pinned by pseudo-version like `ui-auth`. What is a cloud's
own stays in that cloud's module and reaches the framework through hooks: the
error writers (`AWSError`, `S3ErrorXML`, `GCPError`, `AzureError`), the AWS
JSON and Query routers, the sandbox profiles, the console coordinates
(`ConsoleOptions`, through which Azure registers its Microsoft Entra
federation broker), the request rewrites (Amazon S3's zonal virtual-hosted
addressing and Azure Resource Manager's case folding, both
`Config.RewriteRequest`), and the registry behaviours the three registries
disagree on (`BaseResponse`, `RefuseChunkedUpload`, `AdmitRepository`,
`Scope`, `Authorize`, `HydrateManifest`). A Google Cloud method served over
both REST and gRPC shares one implementation returning a gRPC status, which
the REST handler writes through `GCPStatusError`.

A pin must carry the working tree's content, and
`check-support-module-pins.sh` fails when it does not: `go.work` resolves the
tree, so a stale pin passes every workspace build while the installed binary
lacks the fix. A support-module change lands in two pushes — push, pin the
pushed commit, push again — and the pin moves onto the merge commit
afterwards, because a pin on a branch head stops resolving once the branch is
deleted. The scheduled dependency bump follows the same sequence: when
`make upgrade-deps` moves `sim` or `ui-auth`, the bump job pushes, pins the
simulators on that commit, and pushes again.

`sim.Main` is the startup every binary runs, described by a `sim.Program`: the
provider and default port, a `Configure` hook, the cloud's `Build`, and a
`Start` for what serves beside the HTTP port, returning a stop that runs after
the HTTP server shuts down. `sim.Main` binds the listeners before `Build` and
hands the build the resolved callback address: a persistent simulator resumes
its previous process's work during the build (Amazon ECS reconciles, Step
Functions executions, Lambda invocations), and when the address became known
only after the build, the sidecars memoized an empty port and every resumed
task failed. `ConfigFromEnv` reads `SIM_<PROVIDER>_PORT` ahead of
`SIM_LISTEN_ADDR`, so no binary rewrites a placeholder address.

## Mechanisms the simulators share live in sim

Each mechanism the three simulators built alone moved into one cloud-neutral
package, with wire shapes, error envelopes and names left in the cloud's
module. What each move taught:

- **`archive`**: every zip and tar extraction goes through an `os.Root` with a
  size limit; a string-prefix containment check let a sibling directory
  through.
- **`blobstore`, `sparse`, `Server.Payloads`**: an object's contents are not
  in its row, since a row is decoded whole on every read. A write is always a
  new file, written before the row that names it; the replaced file is
  released after, so a crash leaves at worst an unreferenced file the startup
  sweep removes. A file belongs to exactly one row; a copy writes its own. A
  server that does not persist records its temporary payload directories and
  removes them when it stops; it once left over a thousand behind in a long
  session.
- **`simjwt`, `oidcfed`**: one signing-key store and one cached, bounded
  external-issuer verifier.
- **`listq`**: a page token the simulator never issued is refused with the
  service's own error. Offset tokens let a create or delete between two page
  reads repeat or skip an item, so every AWS and Azure list pages through
  `KeyPage` (the last key returned behind a marker byte) and every Google
  Cloud list resumes strictly after the last item's sort values under the
  client's `orderBy` through `ColumnPage`, of which `KeyPage` is the
  one-column case. A token carries those sort values as bytes: carried as a
  string it lost non-UTF-8 keys, which Amplify's pagination test caught.
  `IntKey` replaced the AWS and Azure copies of the integer key spelling.
  BigQuery `tabledata.list` alone keeps a row-index token, because a table
  only grows between truncations (BUG-3448 holds the truncation case).
- **`kvstore`**: throughput token buckets, ordered read-write locks, TTL
  sweeping and change logs, behind Cosmos DB, Azure Table storage and DynamoDB.
  A requests-per-second limiter was rejected as a fake: provisioned
  throughput spends the units the request consumes.
- **`msgq`, `streamlog`, `delivery`, `cron`**: Amazon SQS, Pub/Sub, Azure
  Service Bus, Queue Storage and Cloud Tasks lease from one queue engine;
  Amazon Kinesis and Azure Event Hubs append to one partitioned log; push
  delivery persists each attempt and resumes after a restart; one cron
  evaluator fires EventBridge rules, EventBridge Scheduler schedules and Cloud
  Scheduler jobs in their time zones, Application Auto Scaling scheduled
  actions, Container Apps scheduled jobs and Logic Apps recurrences. `delivery.Post` keeps the receiver's answer, up to a mebibyte,
  so Amazon SNS's delivery-status log records it as `providerResponse`.
- **`lbplane`**: one load-balancer and front-end data plane that never follows
  a target's redirect and grades a probe by the cloud's own matcher. A
  forward that fails after the target's status reached the client says so
  with `ErrAnswerStarted`; callers that inferred it from what the error was
  not read a security-group refusal as a started answer, so three load
  balancers sent 200 and cut the stream where they document a 502 or 503.
- **`signals`**: one keyed change signal in place of nine hand-written
  channel maps, with one rule a waiter follows — take the channel before
  reading the state it guards — which its unit tests prove by racing a writer
  against a waiter. `sim.Announcing` wraps a store so every write announces
  itself.
- **`Wildcard`**: one `*` matcher, with `?`, escapes and case folding as
  options, behind five services' copies. Matchers whose syntax is
  segment-aware or richer (`**`, classes, alternation) keep their own.
- **`dbengine`** with `pgwire` and `mysqlwire`: the managed relational data
  plane of Amazon RDS, Cloud SQL and Azure Database for PostgreSQL. The MySQL
  relay answers an auth switch and, with no TLS to the engine, completes a full
  authentication under the engine's RSA key, so a refused login reaches the
  client as the engine's own 1045.
- **`simkms`**: one AES-256-GCM seal, one RFC 5649 key wrap, and one
  "create the service's key on first use" under the store's `Upsert`. Each
  cloud's ciphertext keeps its own header and ends in the nonce and sealed
  bytes, so values sealed before the move still open. An AWS owned key gets
  its material under the store's lock, since two concurrent first uses once
  sealed under different material.
- **`logstore`**: log entries one row each, keyed by scope and arrival
  sequence. CloudWatch Logs moved from one row per stream that every append
  rewrote; Cloud Logging gained the `_Required` and `_Default` buckets, sinks,
  exclusions, views and routing to other projects, Pub/Sub, Cloud Storage and
  BigQuery; Azure Monitor Logs gained per-table retention. Retention goes by
  each entry's own timestamp, and the per-log row caps went with the move.
- **`dnsserver`**: one listener on UDP and TCP for every simulator; a cloud
  hands it a `Catalog` of the zones a query can see given where it came from.
  The package owns everything that is DNS rather than cloud.
- **`sim/sidecar`, `ipam`, `netrules`, `placement`, `machine`, `vminstance`,
  `nodeagent`, `fileshare`, `workload`, `replicas`, `revisions`**: see
  Execution and The service libraries below.

## Fidelity rules that came from bugs

Each rule below was paid for by a bug; together they are the checklist a new
handler is held to.

- **Answer with the state the call set, not the state its work reached.** S3
  Control's UpdateJobStatus answers Ready while the job runs on; a caller that
  wants the end state polls the status the service exposes.
- **A capture is a resource of its own.** CreateImage records the EBS snapshot
  its mapping names; DeleteSnapshot refuses it while an image maps it.
- **An address belongs to its instance until the instance is deleted.**
  `dbengine.ListenLoopback` keeps an identifier's loopback address through a
  stop; a delete releases it.
- **A delivery leaves a record.** A refused Amazon SNS delivery had left no
  trace, so a test watched 500 ms for an invoke that must not happen; the
  delivery-status log made the refusal observable.
- **A served count is not proof a handler exists.** A multi-segment wildcard
  counted Cloud Storage's per-object ACLs as served while `objects.get`
  answered them; a served method must reach a route naming its literal path
  segments.
- **A status speaks the operation's own enum.** A CloudWatch Logs anomaly
  detector reported `ENABLED`, which its enum lacks.
- **Mail honours a null MX.** A trimmed `.` MX record dialled port 25 on the
  local machine and landed an SNS confirmation in a parallel test's SMTP
  receiver.
- **A method the specification drops leaves the simulator**, as Cloud
  Storage's `channels.stop` did, whose handler had answered 200 without
  stopping anything.
- **A resource group goes with what it holds, and a resource lives in a group
  that exists.** Azure Resource Manager deletes a group as a long-running
  operation through each resource's provider and answers a request beneath a
  missing group `404 ResourceGroupNotFound` before any provider runs.
- **A job's log is its containers' output.** Cloud Run executions once wrote
  "Container started" lines no container printed.
- **A log read that fails says so**, and a drain retries without duplicating
  a line.
- **An unserved operation declares itself** with a 501 naming what is
  missing; the mux's 404 is indistinguishable from a missing resource.
- **A registered operation is not an implemented one.**
  `scripts/classify-sim-handlers.go` marks handlers that answer without
  reaching state, and the surface tables carry the marker.
- **An answer never invents the record it describes.** A member the simulator
  has no basis for is omitted rather than filled; an error reply carries no
  tracking identifier the simulator would have to mint.
- **Read the schema, not the floor comment.** Only a response whose required
  content the simulator would have to invent is declined. Published catalogues
  that can be read deterministically are vendored with source, retrieval and
  counts locked by a test (Azure WAF rule sets, interconnect locations, Cloud
  Armor expression sets, the AWS Price List's instance types, Elastic Load
  Balancing hosted zones).
- **A catalogue and a finding differ.** A published set exists whether or not
  anyone asks, so an empty answer is false; a finding is something an analysis
  detected, so an empty collection is true of a simulator that runs none.
- **Judge a route on every client.** The Go client sends `softDeleted=true`
  and gcloud `softDeleted=True`; `docker push` sends a blob in one `PATCH` and
  Podman sends it on the `PUT`, so Artifact Registry refuses the *second*
  write into a session, which is the chunking Google names.
- **Real clients decide what a document does not list.** Cloud Run's
  condition `reason` is enum-typed but incomplete; gcloud compares against
  the literal `Cancelled`. The validator leaves such fields unjudged with the
  evidence beside it.
- **The simulator is never stricter than the service.** Corrections to a
  model live in `specs/cloud-api/<cloud>/<document>.supplement.json`, each
  pinning the value it replaces and its evidence.
- **The validators judge three dimensions**: unknown fields, an omitted
  required member, and a value outside a declared enum (Azure's
  `modelAsString` enums permit others). Discovery expresses required-ness only
  for requests, so a test drives each insert with a required property removed.
- **Google Cloud wire types read back as Google writes them.** An int64 answers
  as a decimal string; an enum the Go REST clients send as its proto number
  maps to its name through one table-driven `protoEnum` (a number once read
  back as `"2"`, and comparisons with `MANUAL` never matched).
- **A workload's answer is not the cloud's.** The Azure validator skips
  responses served on a site's host or a container app's ingress FQDN.
- **An error shape is attested, never invented.** Where the service documents
  a refusal but not its code, the simulator carries the attested message with
  no leaf code. Each registry's `/v2/` answer and challenge was captured.
- **Two APIs onto one setting are one store.** Blob soft delete behind ARM and
  the data plane; Cloud Run v1 as a projection over the v2 stores; every
  Google Cloud gRPC and REST door over one store, crossed by a test.
- **A state and the event that explains it are one write.**
- **A filter the simulator cannot evaluate is refused**, since an ignored
  filter answers everything and reads as a result; a malformed page token or
  cursor is refused rather than read as none.
- **Optimistic concurrency holds the record until the write is done.** Of two
  writers carrying one etag exactly one writes; Cloud IAM, Secret Manager,
  Bigtable, Organization Policy and Spanner quorum changes each once let
  both through.
- **An asynchronous operation answers as its specification says.** An Azure
  operation declared long-running answers 202 and serves its result through
  the `Location` poll (azcore's no-op poller overwrites a pager with nil on a
  synchronous answer). Compute operations poll at
  `locations/{location}/operations/{id}`; every other provider uses
  `operationStatuses`.
- **A stubbed external dependency fails loudly and names itself.** Amazon SNS
  SMS and mobile push say they need a carrier or Apple's and Google's hosts.
- **A retention the service reports also bounds what the simulator holds.**
  Filtering expired rows out of answers alone let a deployment reach 21,409
  task rows and 1.2 million credentials; sweepers delete through
  `Store.Prune` 500 rows at a time. Where deleting would change an answer,
  the answer moves into what the client presents: a session token carries its
  expiration under the simulator's key.
- **A figure the service refreshes lazily comes from a cache**, as DynamoDB's
  DescribeTable counts do.
- **A resource the service moves through transitional states moves through
  them** (Kinesis consumers, Client VPN policies, resharding streams).
- **Engine work never runs under a store's write lock.** An `Update` callback
  returns what to remove and the caller removes it afterwards.
- **A trust policy is evaluated, and a federated identity verified, as AWS
  does**, against signed tokens and SAML assertions the tests mint
  (`testutil/samlidp`).
- **A request is authorized as the action AWS names, not as its operation.**
  The two differ for 94 operations; the unambiguous ones are generated from the
  Service Reference and the rest resolve from the request, down to each entry
  of a batch.
- **A key counts as built only where code writes its whole name.** A bare-word
  scan credited nine keys nothing built; the measure reads Go string literals
  through `go/ast` and credits only whole names, templated prefixes and the
  tables a populator writes under one service prefix. A generated wire table
  is never read as gate code.
- **Each service spells its keys, tags and errors its own way.** Amazon RDS has
  `rds:db-tag/` and no `rds:ResourceTag/`; tags are read from the member path
  each model serializes; an AWS error carries the message member its model
  declares, resolved per request.
- **A key that says who called is set only when someone else called**
  (`kms:ViaService`, `kms:GrantIsForAWSResource`).
- **A key about a source is read from the source** (a daemon's task
  definition, the DB instance a blue/green deployment clones).
- **An ARN is minted in the Service Reference's format**, so a policy naming it
  matches what the gate derives.
- **A resource that exists implicitly is not minted on reference.**
- **Maintenance may not end the service.** A persistence fault panics in a
  handler, where net/http answers 500; on a background goroutine a busy
  database ends the sweep, which resumes next pass.
- **A stopping simulator lets go of work in flight, and a request ends with
  the server.** Background steps return once their context is done and leave
  their work for the next process; the server's `BaseContext` is cancelled
  before `Shutdown`, so every long poll returns at once.
  `sim.LifetimeContext` gives work a request starts the server's lifetime,
  not the caller's.
- **A create is decided when it is accepted.** The AWS SDK retries a request
  its HTTP client stopped waiting for, and two provisionings of one Amazon RDS
  identifier once ran initdb on one volume; every create and rename claims its
  identifier before checking for the resource.
- **A create abandoned mid-request leaves no container.** A create that fails
  because its context ended removes whatever the engine made under the
  workload's name.
- **A test drives the production write path**, and a listing takes each key
  from the id the object is stored under.
- **An operation answers on every protocol its model declares.** The Go SDK
  speaks only rpc-v2-cbor to Amazon CloudWatch; operations without a CBOR
  route are served from their JSON handlers through generated shapes.
- **An operation names a type that exists.** Every Google Cloud long-running
  operation carries the metadata message its document declares; the
  constructor requires the builder.
- **An event reaches its subscribers because something emitted it.**
- **A subscription stays open as long as the cloud holds it** (Kinesis
  `SubscribeToShard`'s five minutes, declared with `sim.DeclareWait`).
- **AMQP is a byte stream**: a WebSocket message or TCP read can end inside a
  frame. A management reply link never receives the entity's messages.
- **A receive waits for its own event.** Service Bus REST receives, Pub/Sub
  `Pull` and Amazon SQS long polls wake on the entity's change signal and on a
  timer to the next moment the queue changes by itself; StreamingPull checks
  its stream is open before it leases, so a nack racing the client's
  cancellation cannot strand a message.
- **A query runs or is refused, never half-read.** The Log Analytics engine
  types every expression against the schema before reading a row; BigQuery
  once ran DML against a temporary copy and reported success while changing
  nothing, and once honoured one equality in WHERE.
- **A proxy's bound comes from the resource, and idle is not a deadline.**
  `lbplane` keeps `Upstream.Timeout` apart from `Upstream.IdleTimeout`.
- **A load balancer adds its own headers after the hop-by-hop ones are gone**,
  and a platform front end adds only the headers its documentation states;
  undocumented headers seen on the real platforms stay out (BUG-3422).
- **A front end that loses its target mid-response closes the stream**, as a
  streaming proxy does; a failure before the status reached the client gets
  the front end's own error.
- **A page token proves where it came from.**
- **A location's operations belong to the service that started them**, read
  from the Host, since Google serves each collection from its own host.
- **A service a project never enabled is DISABLED.** Answering ENABLED for
  everything meant no harness ever had to enable an API; harnesses now enable
  what they drive on the project they create, as a user does.
- **A project exists before anything lives in it.** Seeding the harnesses'
  project was runner-aware special-casing; every harness creates its project
  through `projects.create`, and each service refuses a missing project with
  its own error.
- **A workload's HTTP server reads the request before it answers.** An `nc -e`
  handler that printed at accept made Go's transport see an unsolicited
  response and answer 503.

## Execution

### Containers, engines and lifetimes

Every workload is a real container (or a guest, below); there is no
host-process path. Each SDK suite scans its module and `sim/` for `os/exec`
and allows each sanctioned call by its exact expression; an allowance whose
expression no longer exists fails the scan. Startup refuses to serve in any
mode that executes workloads when no engine answers: a process that passes its
health check and fails every workload in the background is worse than one
that does not start. Each product's sandbox profile applies, and every
profile refuses the host network and the engine socket.

A workload's stop grace comes from the cloud's own setting (an ECS
`stopTimeout`, Cloud Run's ten seconds, a Container App's
`terminationGracePeriodSeconds`, App Service's
`WEBSITES_CONTAINER_STOP_TIME_LIMIT`); services that publish none keep five
seconds. An AWS Lambda timeout starts when the runtime first asks for work.

The engine allocates every published loopback port and the simulator reads it
back after each start; picking a free port first raced whatever bound it in
between. A resumed container gets a new port, so adoption reads ports after
resuming.

A run's containers are collectable from their labels alone: a detached reaper
collects its own run, the next simulator over the same state directory
collects what a killed one left, and a concurrent suite's workloads are never
touched. `SOCKERLESS_PARENT_PID` is explicit because a guessed `os.Getppid()`
ended a `nohup`ed run when its shell closed; the watch waits on a pidfd on
Linux and kqueue on macOS. A simulator binds its port before printing its
banner, so `Listening on` means the port answers. Every container `sim`
removes goes with `RemoveVolumes`, so the anonymous volumes an image declares
go with it; `StopAndRemoveContainer` returns only once the engine has removed
it. A container create whose name a dead simulator's container holds removes
that container and retries, since a run that died before cleanup leaves
names no live simulator drives.

A product that reports what a workload consumed reads the engine's memory
accounting, whose content depends on the host's cgroup version: on cgroup v1
it carries the kernel's high-water mark (`max_usage`), on cgroup v2 only the
current usage, since the Engine API leaves `max_usage` unset there and serves
no `memory.peak`. The observer keeps the highest of its 50 ms one-shot readings
and reads once more when the figure is asked for; an AWS Lambda REPORT that
read only the polls lost Max Memory Used, or reported an early moment's usage,
whenever the invocation ended before a poll landed after it. A handle's
`StartedAt` is the instant the backend was asked to start the process, which
the process can only follow: a duration counted from `Place`'s return is
negative for a process that answers before that call returns.

A read of a workload's process that the host refuses goes through a platform
helper: the sidecar program joins the container's pid namespace with the one
permission `/proc/<pid>/mem` needs (`sidecar.ReadProcess`). Its proof dumps a
child that has said it runs, because `Start` returns when the exec pipe
closes, before the new program is mapped.

### Execution modes and the no-privilege rule

The simulators once ran workloads two ways: containers on engine networks,
and a host-namespace tier of veths, `nftables` rules and in-process
Firecracker guests needing `CAP_NET_ADMIN`, `CAP_SYS_ADMIN` and `/dev/kvm`,
beside two helper containers that held privilege on the simulator's behalf (a
`CAP_NET_ADMIN` container plumbing an ECS task's ENI address, a `--privileged`
one attaching an EBS image to a loop device). The tier could not run in CI or
on a developer machine, so everything behind it answered a refusal there, and
the two network models diverged: one resolved through the engine, the other
through the cloud's resolver.

The replacement, landed in one pull request so the tree never held both:
three modes under one rule — the simulator holds no host privilege and a
workload container holds no capability. Where a workload runs is a property
of the service in the simulator's configuration (`SIM_PLACEMENT`), never
something a cloud API or a workload sees. `lbplane`, `ipam` and `netrules`
moved from the `realexec` module into `sim`, and `realexec` went. The
privilege a cloud lets a workload ask for (`privileged`, `privilegedMode`) is
not granted on the container backend; what then fails is a `BUGS.md` row a
guest closes.

### Networks, sidecars and the platform network

A VPC, subnetwork or virtual network is an engine network over the resource's
own CIDR, created at first attach (`sim.EnsureCloudNetwork`), and a workload's
address is fixed at attach to the one its interface reports. A bridge network
carries one subnet, so the engine refuses two live networks over one range;
tests therefore name ranges no other test of their suite uses
(`check-test-network-ranges.go`). The AWS CLI ECS tests once probed the engine
for a free /16 at start: two tests in the same moment found the same one. An
Azure virtual network with several prefixes is one engine network per prefix,
since the bridge driver refuses several subnets; an engine network's name
carries the simulator's workload scope so two simulators on one engine never
share one.

The same single subnet is why the sidecar does not sit at the clouds'
link-local addresses: it sits at the address the cloud reserves for its
resolver inside the range, every workload names it with `--dns`, and the
metadata URL travels in the variable the SDKs honour. The sidecar forwards DNS
to a per-network listener that attributes the query to that network — never
to anything inside the message — and metadata to the simulator under a token
minted at start; the trust middleware rewrites the remote address to the
workload's and drops the header, so a workload cannot forge the sidecar.
`sim.Main` opens a plain-HTTP callback listener when the API listener
terminates TLS, because a scratch sidecar carries no trust store.

The platform network — for workloads on no cloud network — is a user-defined
/24 of 100.64.0.0/10 chosen by name, because Docker admits a fixed address only
on a network whose subnet the user configured. Its lower half is the
simulator's to lease (`sim.LeasePlatformAddress`) and the engine allocates
from the upper half (`--ip-range`), so a workload on either backend takes an
address no container can. A lease goes back only once the container that held
it is gone: releasing it when a stop was requested handed the next job an
address the engine still held.

Each workload holds its role through its own service's channel: only an EC2
instance reaches IMDS, Lambda and Amplify compute take sessions in their
environment, and ECS tasks, CodeBuild builds, Batch jobs and Glue runs read
the container credentials endpoint. The AWS SDKs accept a plain-HTTP full URI
only on loopback or the endpoint's link-local address, so a URI naming the
sidecar left every default credential chain empty; the container backend
serves the endpoint on the workload's own `127.0.0.1:51679` through a
forwarder that owns the workload's network namespace. The forwarder is placed
first and the workload joins it, because placing it beside a running workload
raced the SDK's first read and the engine cannot join a namespace before its
owner runs.

### Placement, node agents and guests

Every launch path places through the placement table's backend and holds a
`Handle` — wait, stop, cancel, exec, address, published port, console,
capture — so the container and microVM implementations are interchangeable.
Group-shaped workloads (Cloud Run instances, awsvpc tasks, Container Apps
replicas and jobs, App Service sitecontainers) shared a namespace through the
engine's `container:<id>` mode, so the placement table had nothing to say
about them; `PlaceMember` gave the backend the group's shape.

The node agent is the only container with a device: `/dev/kvm`,
`/dev/net/tun` and `CAP_NET_ADMIN` inside its own namespace, where it bridges
its interface, gives each guest a tap, and boots Firecracker guests from root
filesystems built out of the same OCI images (`mkfs.ext4 -d`). Lessons from
running it:

- Firecracker's API server admits a handful of connections and answers 503
  past that, so the agent's client closes its connection after each request.
- A guest's API socket path is a digest of its ID, to stay within 107 bytes.
- guestinit ended machines with a power-off, which halts a microVM's
  processor and leaves it running for the hypervisor: every guest exit had
  been the agent's fifteen-second backstop kill. guestinit reboots instead,
  which `reboot=k` turns into the reset Firecracker exits on.
- A member's image becomes an ext4 root of its own handed to one of the
  guest's disk slots, which exist from boot because Firecracker takes a new
  backing file for a drive it holds but no new drive; guestinit waits for the
  kernel to see the new size, mounts and chroots into it.
- Each guest process leads its own process group and reports its exit with
  its line count, so a log reader ends exactly when the last line is out, and
  guestinit holds the machine up until the agent has read the console.
- A network the cloud deletes must be left by the agent first: the engine
  removes no network a container is still on, and every ECS test on guests
  hung in VPC cleanup until `MicroVMBackend.Leave` existed.
- A guest has no bind mounts. The guest kernel carries one network
  filesystem, the NFSv4 client, and no pure-Go server spoke NFSv4 read-write,
  so `sim/fileshare` is one; engine volumes are served by a share container in
  the agent's namespace, one per workload, gone with it.
- A guest's further networks are interfaces it boots with
  (`ContainerConfig.ExtraNetworks`), since Firecracker takes interfaces only
  before boot.
- A persistent simulator leaves the agent running and the next process adopts
  it by label and attaches with the token from its environment; `Backend.Find`
  lists workloads by label on both backends, so adoption never reaches around
  the backend.

Security groups, firewall rules and NSGs are enforced on the flows the
simulator mediates through `lbplane`'s `Admit` hooks, and on guests at the tap
through nftables bridge stages each cloud compiles and replaces on every rule
change. Running the clouds' tests on guests found what the KVM proof could
not: CreateVpc had never created the default security group, so everything
joining it carried one empty stage that admits nothing; a transport rule with
no port range rendered a bare keyword nftables rejects; an instance init that
waited on `tee` held its ready marker for ever behind a daemon keeping the pipe
open; an instance naming a subnetwork alone landed on the default network.

NAT gateways are egress sidecars holding `CAP_NET_ADMIN` in their own
namespace, redirecting TCP and UDP to a proxy that dials from the gateway's
address. A datagram's original destination comes from the connection tracker,
because `IP_RECVORIGDSTADDR` reports the rewritten one; a single-entry
conntrack query hung under mdlayher/socket v0.6, found by bisecting the
dependency bump, so the sidecar dumps its namespace's table instead.

### VM instances and cloud-init

`sim/machine` holds the lifecycle the three clouds each wrote: boot and wait
for the init's ready marker, watch, stop keeping the disk, start, discard,
adopt, capture and the console across boots, with each cloud's state names as
hooks. Merging the copies fixed what they did differently: a failed start
leaves an EC2 instance stopped with its disk, a Compute Engine stop during
boot owns the status instead of unwinding the insert, an Azure guest that
never becomes ready is stopped rather than left running, and the readiness
polls and five-minute boot timers the clouds have no counterpart for went.

An instance's init runs the image's cloud-init over the user data, rendered at
placement into `/var/lib/cloud/instances/<id>/` so a new instance from a
captured image runs its own user data while a restarted one keeps its
semaphores. The guest library is POSIX sh and edits `/etc/passwd` and
`/etc/shadow` itself, because Amazon Linux 2023 has no shadow-utils. Compute
Engine's `startup-script` runs at every boot after cloud-init; the init honours
systemd's SIGRTMIN+4 and busybox's SIGUSR2 so a guest's own poweroff ends it.
The metadata services read the instance's current record; each cloud's
metadata service answers its own workloads and nothing else, since an
invented default machine answered every other caller.
An EC2 launch takes each metadata option from the launch, then the account's
`ModifyInstanceMetadataDefaults`, then (tokens and hop limit) the AMI, then
EC2's default, as the vendored options page orders them, and an account that
enforces IMDSv2 refuses an IMDSv1 launch with `UnsupportedOperation`. Each
Amazon ECS container has an `ECS_CONTAINER_METADATA_URI_V4` of its own, which
answers that container's record, the engine's stats at `/stats` and the task's
at `/task/stats`; a container that has not started has no stats, because the
engine's one-shot read waits on it. Amazon ECS issues no
`AWS_CONTAINER_AUTHORIZATION_TOKEN` (EKS Pod Identity does), so neither does
the simulator. Each awslogs container writes to its own
`<prefix>/<container>/<task>` stream; a shared sink once put every
container's lines in the first one's.

The Google Cloud metadata server resolved every read against one tree, so a
listing named nothing that did not answer, and each workload read only the
keys its service documented: a Compute Engine instance its predefined keys,
a Cloud Run container the id, region, `<region>-1` zone and Direct VPC address
its container contract states, a Cloud Build step no instance keys at all.
The fixed `sim-instance-1`, `1000000000001` and `us-central1-a` answers went,
since a fixed answer reads as a real instance. The vendored Compute Engine
pages set `alt`, the token's enforced scopes, the identity token's `full`
format and the guest attributes a guest writes; scopes came from the
instance's service account rather than a constant `cloud-platform`.

The Azure Instance Metadata Service follows its vendored Learn pages
(`imds-instance-metadata-service`, `imds-scheduled-events`,
`imds-load-balancer-metadata`): each category refuses an absent or unserved
`api-version` with the documented body and its three newest versions, route
parameters walk the document to any node, a leaf answers only with
`format=text`, and the network section carries each IP configuration's public
address. Load balancer metadata lists the Standard load balancers' rules that
reach the machine's interface. Scheduled Events turn on with the machine's
first request and off after 24 hours without one; while on, a restart, a
redeploy and a Spot eviction schedule a Reboot, Redeploy or Preempt event with
the documented minimum notice and wait for the guest's `StartRequests`
approval or `NotBefore`, so a machine that never asks keeps the immediate
operation.

A VM's `/run` is volatile: each boot starts with an empty one
(`SandboxProfile.VolatileRun` mounts a tmpfs, as the microVM guest init
already did). On the root disk, the readiness marker, cloud-init's
`result.json` and the per-boot status files survived a restart, so a rebooted
instance reported ready before its second boot ran.

## Services

### App Service and Azure Functions

A site runs its image as App Service runs a Linux custom container, on
`WEBSITES_PORT` (80 unset), started by a request or Always On and bounded by
`WEBSITES_CONTAINER_START_TIME_LIMIT`. Built-in stacks run the platform's
`mcr.microsoft.com/appsvc` images pinned to one build; a Linux `Node|22`
function app runs the Azure Functions host with the platform environment
without which the host neither runs in App Service mode nor writes keys.
Function keys are written as the host writes them — ASP.NET Core Data
Protection payloads and identifiable secrets with the `AzFu` signature — so
the host reads what the ARM key operations wrote (BUG-3237 holds the rest).

The SCM site serves Kudu at the app's SCM host, routed by a generation index
so every read answers from an index. Deployment, WebJobs (with NCRONTAB
schedules that, like Kudu's, run none missed while down), the VFS, the command
API, settings and the log stream follow Kudu's controllers. The VFS once
declared a growing file's stat size and copied the whole file, overrunning
`Content-Length`; it stats the open file and copies exactly what it declared.
Slots run their own containers and swap with sticky settings; `/home` is one
persistent share the app, Kudu and every instance mount. Key Vault references
resolve as the app's `keyVaultReferenceIdentity` under the vault's own
authorization decision and are re-fetched every 24 hours.

### Cloud Run and Cloud Run functions

A service is served at its own run.app URL behind a Google-signed ID token
whose principal holds `run.routes.invoke`, conditions included; an access
token answers 401. Probes and traffic go to the container's own address where
the host can route to it, found from the host's interfaces rather than by
connecting, because the engine's published port accepts before the workload
listens (BUG-3242 holds hosts that cannot route). A Cloud Run function is the
Cloud Run service CreateFunction creates, also served at its
cloudfunctions.net URL.

`RunJob` answers with an operation that stays running while the execution
runs and completes from the execution's settle; `operations.wait` blocks on
the signal `gcpFinishOperation` raises. A restarted simulator keeps every
worker pool's and instance's workloads running, as Cloud Run's control plane
never restarts what it runs, and adopts whole instances of the current template
by label and template digest; adoption precedes the recovery that fails
executions without a workload. A cancel records its condition before stopping
instances, since a stop that settled first once marked a cancelled deploy
failed. Structured JSON log lines become `jsonPayload` with the Logging
agent's special fields lifted, through one builder for every sink.

An instance is ready the moment its ingress container passes its startup
probe, at the route read while that container ran; reading the route again
after the probe turned a container that exited right after passing into a
failed start instead of a ready instance's exit for the restart policy. A
`CancelExecution` operation stays running until the execution's tasks have
stopped and completes with the settled execution, kept apart from the
`RunJob` operation that reports a cancellation as its error; done early, it
let the execution's etag change after the caller read it. Every Knative list
carries `metadata`, with `continue` only when more pages exist, because
gcloud reads `metadata.continue_` without checking.

### Cloud Tasks and Cloud Scheduler

Both are served from their vendored Discovery documents on the shared
libraries, as the AWS and Azure counterparts were. A Cloud Tasks queue's
tasks live in a `msgq` queue: the next attempt is the message's availability
and a dispatch its lease for the dispatch deadline, so a simulator that stops
mid-dispatch dispatches the task again once the lease lapses. Dispatches wake
on the queue's signal and next-change timer — a schedule or retry time, a
lapsed lease, the token bucket's next token — never on a ticker. A Cloud
Scheduler job fires on `sim/cron` in its time zone and delivers through
`sim/delivery`, which gained `Policy.BothLimits` because Cloud Scheduler
retries until both `retryCount` and `maxRetryDuration` run out, and
`Request.Method` because both services send the target's own method. The two
share their Google retry backoff (doubling `maxDoublings` times, then linear,
capped), their push authorization and their HTTP-status-to-code mapping.
What no vendored page states is a `BUGS.md` row, not a guess written as
fact.

### Image pulls and build services

A workload host pulls its image as the cloud pulls it: Cloud Run presents its
service agent only to Artifact Registry and Container Registry, Azure hosts
pull with what the workload declared, the Lambda and ECS hosts resolve
pull-through-cache references through ECR. A pull asks the host first, and a
registry's data-cap refusal is not retried. Build services give their docker
steps a Docker configuration built on the host's own whose credential helper
answers the build's registries with the build identity's token; it names those
registries outright, because the legacy `docker build` sends the daemon only
the credentials of registries the configuration names. Every docker
invocation interrupts on cancel and bounds the unwind with `WaitDelay`, since
a killed CLI never tells buildkit to stop. ACR Tasks' `scheduleRun` answers
200 with the run at once — the Azure CLI accepts nothing else — and runs it in
the background; the run engine drives steps through the Engine API and keeps
only `docker build` as a CLI call.

### Amazon ECS, EC2 and VPC placement

RunTask commits each task's declared memory and CPU against the simulator's
own cgroup or machine, with in-flight reservations in the ledger so two
concurrent RunTasks cannot both take the last slot. Amazon EC2 Capacity
Manager's supportability scores read the same host: what its CPUs and memory
hold once running instances and placed tasks take theirs, divided by each
instance type's size, banded high, medium and low against the requested amount
as AWS documents. Every Availability Zone the simulator serves shares that
host, so zones score alike and the aggregate is the best combination's score. The
same host bounds a launch by memory: `RunInstances`, `StartInstances` and
Auto Scaling refuse an instance the host's free memory cannot hold with
`InsufficientInstanceCapacity`, the server error the vendored error-code page
lists, while vCPUs overcommit as a hypervisor's do. Spot placement scores read
it too, 10 at three times the target and linear below, never under 1.
A task belongs to the
deployment that started it by `startedBy`. An awsvpc task creates its
interface at `RunTask`, so DeleteSubnet refuses while it holds it. The task
metadata endpoint waits for its task's start to settle, because a container
that read it at once found a 404. Express Mode and gateways build their load
balancers, certificates and scaling through the services' APIs as the
infrastructure role; writing straight into another service's store skipped
that service's rules.

A regional NAT gateway is a VPC-level gateway. In automatic mode it takes an
address in every zone that has a subnet and follows subnets as they come and
go; with `AvailabilityZoneAddresses` it covers exactly the zones and Elastic
IP addresses given. Its data plane is the zonal gateway's: one egress sidecar
per covered zone at an address in that zone's subnet, and each subnet routed
to the gateway leaves through its own zone's sidecar. What the EC2 model
leaves unstated is inferred from terraform-provider-aws's acceptance tests,
which run against AWS, and recorded as a bug until a capture replaces it.

A NAT gateway reports its `availabilityMode`: terraform-provider-aws reads a
zonal gateway's addresses only when the mode says zonal, so a gateway without it
left `public_ip` empty.

Every service that takes a role refuses one IAM does not hold or whose trust
policy does not admit the service (`iamServiceTrustedRole`), and tests that
had passed invented role ARNs create real roles. A trust policy's `Service`
principal matches the regional name as well as the global one.

### Managed databases

Managed caches run as managed databases do: `sim/cacheengine` runs real
Valkey, Redis and Memcached nodes on `placement.Backend` behind loopback
endpoints the simulator owns, as primary/replica instances or Redis Cluster
shards, with resharding, failover, reboot, restart and adoption, and TLS
required, preferred or off per endpoint. Memorystore for Redis moved onto it
unchanged. Amazon ElastiCache clusters, replication groups and serverless
caches report `creating` until their engine answers, and report the
endpoints it serves; a serverless migration copies the source's keys. Azure
Cache for Redis serves its `hostName` on TLS 6380, and on plaintext 6379
when enabled, to either access key.

Amazon RDS, Cloud SQL and Azure Database for PostgreSQL run real engines on
named volumes, readiness classified by SQLSTATE (57P03 is not ready whatever
the socket answers). Lessons:

- **A capture is crash-consistent.** `sim.SnapshotVolume` freezes every
  container that mounts the volume writable through the cgroup freezer for the
  length of a reflink copy; the Engine API refuses `exec` into a paused
  container, so engine commands run under `sim.HoldThawed`.
- **A MySQL base backup is stored recovered.** Crash recovery commits prepared
  transactions only when run with the binary log, so the capture runs the
  engine on the snapshot with its own arguments until it accepts connections
  and shuts it down cleanly.
- **Point-in-time restore replays the engine's own log**: PostgreSQL archive
  recovery to `recovery_target_time`; MySQL, whose image ships no
  `mysqlbinlog`, by cutting the binary log at the first transaction after the
  time and applying it as a relay log; MariaDB by its GTID events, which date
  in whole seconds.
- **A deletion discards the engine** rather than stopping it, because the MySQL
  entrypoint ignores SIGTERM while it initialises.
- **IAM authentication on MySQL-family engines** needs a server plugin the
  stock images lack, so the simulator ships `AWSAuthenticationPlugin` as C
  with no C library dependency, compiled on the engine per ABI and loaded from
  the data volume so snapshots carry it. PostgreSQL passwords other than the
  master's are checked against `pg_authid` by a `DO` block with the values
  passed as settings, never spliced into SQL.
- **Parameter catalogs are the engines' own**, captured by
  `scripts/capture-rds-parameter-catalogs.go`; PostgreSQL takes dynamic
  parameters from a file, because a reload never overrides a command-line
  setting.
- **Every engine version runs its release's image**; a PostgreSQL major
  upgrade needs `pg_upgrade` with both releases' binaries, which no official
  image carries (BUG-3398).
- **Replicas, Multi-AZ standbys and blue/green greens** are real engines on
  the engines' own replication. Every PostgreSQL primary start resets
  `synchronous_standby_names`, since the data directory carries it into
  copies.
- **Events come from a store wrapper** (`rdsObserve`) that sees every write,
  so a status change records its event whichever code path makes it, with
  the vendored event table's messages.

Memorystore for Redis runs one Redis container per node. Settling waits on the
engine, never a simulator timer: every node sends `CLUSTER MEET` to every
other before the topology is read, since a node learns a peer's role only from
a packet the peer sends. A cluster reshape moves slots with Redis Cluster's
own commands.

### Storage

An object store's conditional write is one step: evaluation and store under
one per-object lock (`sim.KeyedLocks`) at each service's single write path,
because a client keeping its consistency in an object store relies on exactly
one of two conditional writers winning. The object stores are
`sim.PrefixStore`s, which have no whole-store read; the store-scan gate counts
scans, not bytes, so the type is what keeps a listing to its prefix. Amazon S3
keeps the current version in `s3Objects` and every other version apart, with
one write path applying versioning and Object Lock. Buckets a service hands
out presigned URLs into belong to an AWS-owned account, signed with a
credential only the simulator process holds; kept in the caller's account they
were listable and overwritable.

A Cloud Run Cloud Storage volume writes back as Cloud Storage FUSE does
without a FUSE file system: inotify events prompt a comparison with the
simulator's view of each path, every change the simulator makes happens under
one mutex, and a barrier makes each JSON API request wait for the events
queued before it. Files open with `RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS`
(BUG-3084 holds the rest).

Azure Storage's CRC64 is computed in simulator-azure, with the polynomial
and little-endian encoding the azblob SDK implements, rather than in
`sim/blobstore`: it is Azure's alone. Which checksum header a write answers
with depends on the request's service version (`x-ms-version`, then the SAS
`sv`, then the account's `DefaultServiceVersion`), because Azure documents
three regimes (before 2019-02-02, from 2019-02-02, from 2026-10-06). Where the
vendored Swagger and the REST reference disagree, as on Put Blob's CRC64, the
reference wins, as the newer SDK's generated client also does.

Put Block List and Set Blob Properties store the blob's Content-MD5 only from
`x-ms-blob-content-md5` and clear it otherwise, and Put Block List's response
`Content-MD5` covers the block list, as the Blob Storage specification states.
azurerm uploads a blob and then sets its properties without the header, so a
Content-MD5 that survived either step drifted its `content_md5` on every plan.

A Cloud Storage bucket reports its `locationType` (multi-region,
dual-region including the predefined pairs, or region), which a relocation
updates, and an object written without a storage class takes the bucket's
default.

State a cloud derives with nobody calling — a future-dated Capacity
Reservation becoming active — is derived on every read rather than stored,
since a timer could be late. An Azure cross-resource-group move rewrites
references by walking every tracked store, because a hand-kept list rots.

### Authorization and identity

Azure RBAC decides every Azure Resource Manager operation: the bearer gate
runs before every slice, derives the action from the path and verb as ARM
names actions, and evaluates `actions` and `notActions` over the caller's and
its groups' role assignments up to the tenant root management group. The
bootstrap service principal is Owner through a seeded assignment, not a
bypass. Storage authorizes bearers by data action, honours
`allowSharedKeyAccess: false`, and verifies user delegation SAS against its
key; Key Vault RBAC and Event Grid publish share the evaluator. A test
principal gets exactly the role assignments the real cloud would require.

Every surface that the cloud authenticates, the simulator authenticates:
- the AWS REST management services (AWS Amplify, Amazon API Gateway, AWS
  Batch, Amazon EFS, EventBridge Scheduler, Amazon CloudFront, Amazon
  Route 53) verify SigV4 before IAM, and answer the front end's own 403
  codes;
- Amazon S3 treats an unsigned request as the anonymous principal, which
  only a Principal `"*"` bucket policy or an AllUsers ACL grant admits, and
  Block Public Access withdraws;
- the Google Cloud gRPC listener verifies every call's bearer, over a TLS
  front end because Google's clients send credentials only over TLS;
- Google Cloud accepts a service account's self-signed JWT, as google-auth
  sends by default;
- Microsoft Graph requires a token with Graph's audience, and the password
  grant checks a verifier of the password Graph stored.

Each shortcut these replaced let a test pass without the credential its
client sends against the cloud.

Each simulator's first credential is seeded, the way a new account's is: the
AWS simulator's IAM user with the `test` key, the Azure simulator's bootstrap
application with its client secret, and the Google Cloud simulator's bootstrap
service account `sockerless-bootstrap@sockerless.iam.gserviceaccount.com`
with a published RSA key that IAM reports through `serviceAccounts.get` and
`keys.list`. Clients sign in with it the real way: gcloud's
`activate-service-account`, Application Default Credentials, the provider's
`credentials`. The token endpoint therefore refuses a JWT-bearer grant
without an assertion, as Google does; minting a token for one let every
suite skip the flow its clients use against the cloud. The credential file
is written on demand (`testutil/cmd/gcp-bootstrap-key`), never committed.

AWS IAM derives each action's resources from the Service Reference's ARN
formats; identifiers end at the next `/` except names AWS documents as paths.
A create carrying tags is authorized twice, as itself and as the tagging
action, from a generated table of 297 operations. The REST services' routes
pass the gate through `awsRESTRoutes`, compiled from the Smithy `http` traits.
Each condition key is resolved where the service documents its value or
classified with a reason, and `TestIAMConditionKeyCoveragePerAction` renders
every served operation from its model to prove it per action.

Azure Key Vault authenticates and authorizes every request in Key Vault's
order: token, network rules, then access policies or RBAC data actions, with
App Service's reference resolution reusing the same decision. Managed
identities get their own `IDENTITY_HEADER` per app, slot, container app or
job.

Google Cloud IAM once decided a call in four places only, so a service
account holding `roles/storage.objectViewer` deleted objects. One gate
(`iam_gate.go`) now fronts every authenticated REST and gRPC method: it
resolves the call to its Discovery method, takes the method's permission from
the first source that names one (the method tables Cloud Storage and Pub/Sub
publish, each method's REST reference page, a cited per-method table, the
service's audit-logging page, then Google's `service.resource.verb`
convention), and evaluates the policies on the
resource and every ancestor up to the organization. A gRPC call is decided as
the REST call its `google.api.http` binding spells. The bootstrap account is
an Owner through a seeded organization `roles/owner` binding rather than an
email check, and basic roles grant no impersonation, as on Google Cloud. A
method no source covers admits only an Owner, and `BUGS.md` counts them. A
bucket's `projectOwner:` convenience member matches project bindings the way
every other binding does, so a workforce pool's principal set granted Owner
covers the pool's subjects on the project's buckets too. A project's creator
becomes its Owner, as the Project Creator role documents, and a Resource
Manager operation poll reads the project it produced, since Google publishes
no permission for it and a Project Creator polls its own create.

A REST reference page states its method's permission in prose that Discovery
does not carry, so `scripts/fetch-gcp-method-permissions.go` compiles the pages
into `method-permissions.json.gz` once, with each page's "Last updated" date.
Its rows tell a verb apart from the convention's guess: Compute Engine's
`instanceGroupManagers.listManagedInstances` needs `get` and `resize` needs
`update`. Re-fetching several hundred pages on every freshness run costs more
than it buys, so the freshness check samples a rotating slice of them each day
and reports a page dated after its row.

AWS KMS serves ML-KEM (FIPS 203) key pairs: `ML_KEM_512`, `ML_KEM_768` and
`ML_KEM_1024` keys with `ENCAPSULATE_DECAPSULATE` usage, kept as their 64-byte
seed, answer `Encapsulate`, `Decapsulate` and `GetPublicKey`, whose
SubjectPublicKeyInfo carries the NIST `id-alg-ml-kem-*` OID. The SDK test
encapsulates with Go's `crypto/mlkem` against the simulator's `circl`, so two
implementations agree on every secret. The CLI suites and the AWS documents
install the current AWS CLI v2 through `scripts/install-aws-cli.sh`, since the
runner image's CLI predates `aws kms encapsulate`.

Google Cloud IAM answers `testIamPermissions` from the stored policy, with
`cel-go` evaluating conditions against the attributes the IAM Conditions
reference lists per resource type — a resource outside the reference supplies
none, as on Google Cloud. A deleted service account lives in a store keyed by
unique ID, because a new account may take the email during the window. A
custom role once never granted, because the lookup used its id where the
store keyed by full name.

### Work that settles behind the request

AWS work the services finish later finishes later in the simulator, gated on
real inputs: an EC2 instance stays pending until its machine boots, Auto
Scaling answers at once and boots behind, S3 Batch Operations jobs move
through their statuses as background work and resume after a restart, AWS
Batch schedules by fair share and decayed usage. Azure answers and settles
behind (Event Grid validation handshakes, Logic Apps runs, Container Apps
revision provisioning). Google Cloud's builds and BigQuery jobs record
PENDING or QUEUED, answer at once and run behind their operation.

Simulator code waits on the event a wait stands for: a workload's exit, the
engine's removal of a container (`ContainerWait` with the `removed`
condition, which `sim.WaitContainerRemoved` and `sim.RemoveVolumeSettled`
use), a `sim/signals` change signal, a drained serial console. Where nothing
raises an event, it polls the engine's own status on a short cadence and ends
at once on the workload's exit: a port that starts listening (Amplify Hosting
compute, App Service warm-up) and a replica's replication starting (Amazon RDS
read replicas and Multi-AZ standbys, whose engines report it only in
`SHOW REPLICA STATUS` and `pg_stat_wal_receiver`). The remaining tickers are
cadences the clouds document.

## The service libraries

Stage 5 of the plan moved the mechanisms the clouds' compute, eventing and
queueing services share into `sim`:

- **Seams.** A service acting on another's behalf calls its API in-process
  through `sim.Server.Call`, the whole chain a client's request takes, as the
  service principal `sim.Caller` names or a session of the role, signed with
  SigV4 as the SDKs sign. The verifier checks a stated payload hash against
  the body, so a signature no longer survives a swapped payload. EventBridge,
  Scheduler, Lambda's SQS pollers and destinations, Step Functions tasks and
  `.sync` waits, CodeBuild batches, Eventarc and Event Grid dead letters all
  act this way; Step Functions `.sync` wakes on the service's store writes and
  confirms through the API rather than polling.
- **`sim/machine`** (above).
- **`sim/workload`, `sim/replicas`.** Groups start in `dependsOn` order behind
  startup checks; a replica set keeps a count running, restarts or replaces
  with back-off, and adopts after a restart. Cloud Run worker pools and service
  revisions and Container Apps revisions run on it; a Container Apps replica
  restarts in place keeping its name and address. A replica still starting
  counts toward the set's count: `Scale` and `Raise` once counted only the
  replicas the set held, so a Cloud Run service whose minimum and then manual
  instance count changed while its first instances passed their startup
  probes started extra instances and stopped them as surplus.
- **`sim/revisions`.** A template's identity by content and the choice of a
  revision by traffic weight, for Cloud Run (both surfaces, which once minted a
  revision on every write) and Container Apps.
- **`sim/msgq`'s waiter.** Amazon SQS, Pub/Sub and Service Bus each wrote the
  same long poll: a per-queue signal plus a timer to the next moment the
  queue changes by itself. `msgq.Waiter` is that algorithm once, and
  `Queue.NextChange` names the moment. Service Bus's per-message timers became
  the waiter's self-re-arming `Arm`. Dead-letter routing, sessions and
  message moves stayed per cloud, since each is one cloud's concept.
- **The host-routed front end.** `lbplane.HostRouter` claims a request by
  its Host and asks the cloud's `Route` hook, a generation-index lookup, for
  the handler; `lbplane.Serve` is the one forward with a front end's own
  answers. A Lambda function URL routes to an invoke handler instead of a
  forward. The probes moved with it: `replicas.Probes` runs startup,
  liveness and readiness with the kubelet's semantics, and
  `replicas.CrashLoopBackoff` holds the kubelet's back-off, since Cloud Run
  documents no back-off of its own.
- **`sim/eventbus`.** The fan-out loops were small and alike; the filter
  dialects, input transformations, payloads and target calls were not, so
  they stayed per cloud behind hooks. Amazon SNS delivers every protocol
  through one dispatcher with the retry policy SNS documents for that kind of
  endpoint, and a notification that fails for good reaches the
  subscription's dead-letter queue. An EventBridge input transformation is
  applied once, at publish, and stored with the delivery. A queue that owns
  its retries — a Pub/Sub subscription, an Eventarc transport topic — is a
  `Sink`, so push keeps sharing its retries with pull.
- **`sim/jobrun`.** An execution of N tasks with parallelism, per-attempt and
  per-task timeouts, retries, a completion policy, cancellation and
  restart resume, with no store and no wire shape. Cloud Run jobs run one
  container group per task attempt with the documented `CLOUD_RUN_*`
  variables; Container Apps jobs run replicas until `replicaCompletionCount`
  succeed and resume after a restart instead of staying Running for good.
  Amazon ECS `RunTask` and AWS Batch kept their schedulers: ECS tasks are
  independent with no execution, and Batch's RUNNABLE accounting would change
  under the library's loop.
- **`sim/funcrt`.** An AWS Lambda execution environment keeps its container,
  Runtime API listener, address and log stream for its life, so a handler's
  globals persist and sequential invocations share a stream. Its Init phase
  runs from the container's `StartedAt` to the runtime's first Next request,
  and the REPORT's Max Memory Used is the environment's peak read when the
  invocation ends. A unit returns
  to a concurrency limit only once its environment is idle again, so the next
  invocation reuses it instead of starting another. AWS documents no idle
  timeout, so an environment retires on an update, a delete, an alias change
  or before its role session lapses, never on a timer. A throttled
  asynchronous event retries under `delivery.Deferred`, which spends no
  attempt. Azure Functions and Cloud Run functions stayed off the pool: the
  first dispatches inside one long-lived host container, the second is
  already `sim/replicas`.

## Consoles

The three consoles were rebuilt on their real consoles' patterns — the AWS
Management Console's Console Home, Services menu, unified search, Region
selector and per-service navigation; console.cloud.google.com's project
picker, navigation menu, URL structure, filter bar and full-page creates; the
Azure portal's top bar, browse and resource blades and create wizards — still reading only real cloud APIs. Decorative controls nothing
backed (notification bells, support icons, a hardcoded "Service health"
badge, "Not supported" product pages) went, because a control that does
nothing is a fake. A console gap whose API the simulator does not serve is a
`BUGS.md` row naming that API.

- **Sign-in is a deployment's.** The AWS and Google Cloud browser suites
  sign their operator in through simulator-azure's Microsoft Entra ID and
  federate through the cloud's own primitive, provisioned by
  `global-setup.ts` through the real APIs; the Google Cloud vitest tests that
  mocked fetch for the rebuilt pages went in favour of browser flows. ui-auth verifies `at_hash` only when the
  issuer sends one: OpenID Connect Core makes it optional in the code flow,
  Entra omits it, and a console signing in through Entra got a 403 at the
  callback.
- **The signer is the SDK's.** The console's SigV4 signer sends
  `x-amz-content-sha256` to Amazon S3 and builds the canonical query string
  sorted and RFC 3986-encoded, as SigV4 defines it; without the header S3
  cannot verify a request that has a body.
- **Isolate the browser from the host's interfaces.** Chromium's Linux
  network-change notifier treats every rtnetlink address message on its host
  as an IP address change, with no switch to narrow it, and flushes its
  socket pools with `ERR_NETWORK_CHANGED`, aborting every request still
  waiting for a connection. The simulators' workloads add and remove engine
  bridges throughout a run, so a `page.goto` failed at random. Chromium runs
  under `unshare --user --map-root-user pasta --config-net --tcp-ns <ports>`
  (`isolated-chromium.sh`): it sees only its loopback and a tap nothing
  changes, and reaches the console and its identity provider on the host's
  loopback at unchanged URLs. The leading user namespace lets pasta map UID 0
  as root or not. The job runs on `ubuntu-24.04`, the release the pinned
  passt package and its AppArmor profile come from: on Ubuntu 26.04 the
  profile refuses pasta its user namespace. Retrying or serialising the tests would have left the cause
  in place.
- **Stop the webServer with SIGTERM.** Playwright kills its webServer's
  process group with SIGKILL by default, so the backend script's exit trap
  never ran and every run leaked its simulator build and the simulators'
  containers and networks; `gracefulShutdown` sends SIGTERM.
- **Attach document-level shortcut listeners before paint.** The Alt+S and
  "/" search shortcuts attach in `useLayoutEffect`, so a key pressed right
  after load is never lost.
- **A write's refresh starts after the write.** TanStack Query joins an
  invalidation onto a fetch still in flight when the query has no data yet,
  so a repository created while the list's first `DescribeRepositories` was
  in flight closed its dialog over an empty list. Every console builds a
  `ConsoleQueryClient` (`@sockerless/ui-core/query`), which cancels that
  fetch before it invalidates, and `check-console-query-client.sh` refuses a
  plain `QueryClient`.
- **Audit settled colours.** An accessibility audit that ran during a
  transition measured a frame's contrast, so each console installs a
  no-transitions sheet as an init script before the audit and waits on the
  element's settled state, never on a sleep.

## Measurement and gates

Every quality gate has been shown to fail on a planted violation of its own
shape, and a gate whose scan set can go empty exits non-zero: two gates once
scanned the monorepo's directories, found nothing, and hid two live
slice-bounds panics. A floor counts only what it measures, and generators
sort under `LC_ALL=C`.

Attribute a slow phase by measuring where the simulator runs, never by reading
code. A duration says only "fast today", so concurrency fixes are proven by
counting or by construction; a lock test whose broken lock blocks the test's
own channel fails the same way on every run, where one bounded by a clock
measures the machine. `RLock`/`Unlock` mismatches, which neither the compiler
nor `go vet` sees, are held at zero by a gate.

The slow-request diagnostic reports a request only once it outlives the wait
its handler declared (`sim.DeclareWait`, `sim.DeclareOpenEndedWait`); the
framework holds no list of slow methods. `bg.Go` and `bg.AfterFunc` count
background work and a drain (`bg.Await`) is a barrier that stops unfired
timers and drops work requested while it runs, because reconciliation
requests more whenever it moves a task; arming a timer holds a read lock a
drain's write lock excludes.

A re-vendored model can name operations newer than the pinned SDK; such an
operation takes a scoped drift exemption citing its `BUGS.md` row, and a test
fails once the row closes or the operation is served.

## Testing discipline

- **A document's commands are tested as written.** `testutil/doctest` runs
  every shell block of a tutorial in order, as a reader runs it, and checks the
  output the text promises. Its first runs found commands that had never
  worked: `@latest` resolved the simulators and `gcp-bootstrap-key` to a
  bootstrap `v0.1.0` the module proxy still serves; the Google Cloud stack
  deployed into a project only the Terraform suite's `TestMain` created; the
  contributing tutorial ran a microVM-only test. Every shell block in the
  repository carries an annotation, so a document gains no command nothing
  runs.
- **Console tests run against the real simulators.** The console unit tests
  start the simulators and sign in through Microsoft Entra ID as the consoles
  do; the mocks they replaced had hidden a SigV4 signer that encoded paths
  once where every service but Amazon S3 needs them encoded twice.
- **Comments say what the code cannot.** A pass over every package cut
  restatements, banners, narration, history and bug IDs, put the rest in
  active voice, and corrected comments that disagreed with their code. The
  pass surfaced the fidelity gaps filed as BUG-3778 to 3791.

- **A test waits on the event it asserts**: the SDK's own waiters with short
  delay bounds, `aws … wait`, long polls, Live Tail opened before history is
  read, an operation's `Wait`, azcore's poller (`awaitARMOperation`) and
  `az rest` driven through `azLongRunning`. A Cloud Run job that must be seen
  running holds until the test writes a release object into a bucket it
  mounts. The waits left are the cloud's own (BUG-3154).
- **Count what you mean to count.** An ECS replacement of a stopped
  replacement is the scheduler working; two in flight for one failed task is
  the bug.
- **Tests run in parallel** unless `serial-tests.txt` names why not, and name
  their resources per run (`uniquename.New`, `uniquename.Alnum`: eight hex
  digits from a per-process random start, distinct for every call); an account-wide
  setting is restored at cleanup and never asserted by list length. Shared
  account singletons (the Glue Data Catalog policy) are held by a package
  mutex from put to delete.
- **A boundary is asserted at a small parameterised limit**, a fuzz failure
  becomes a seed, and a soak test sizes its pool to the machine.
- **Drain `sim/bg` before swapping stores.** A test fixture that replaces
  store globals calls `bg.Await` first and as its last cleanup: a Service Bus
  scheduled-message timer armed by one test once fired into the next test's
  stores. A unit test also passes alone and twice in one process; two failed
  only after another test had set state up, or on their second run.
- **A tool a suite needs is installed in `TestMain`**, never skipped: gcloud
  with its `alpha` and `beta` tracks (installing a track on first use ate each
  test's budget), `cbt`, the AWS CLI.
- **Ports a suite hands out are picked together** (`testutil/freeports`),
  below the ephemeral range when a restarted simulator must bind them again;
  a DNS port comes from `freeports.TCPUDP`, free on both protocols on every
  interface.
- **A tutorial is a test.** Each cloud's tutorial configuration is the
  fixture its Terraform test applies, and the test checks the outputs the
  tutorial tells the reader to look for, so a step that stops working fails
  CI. Its function and container steps check real work: the Lambda
  function's return value, the Azure function's response, a Fargate service
  in steady state.
- **A helper two suites need lives in `testutil`**, never as a copy per
  suite: copies of the workload image builder drifted until only five of
  seven built with `GOWORK=off`. What stays per suite is what sets one
  cloud's environment (`runCLI`, `terraformCmd`).
- **smithy-go's closed request body** answers `io.EOF` from `WriteTo`, and
  net/http's extra read then closed connections under parallel event streams;
  the AWS SDK suite hands the transport bodies without `io.WriterTo`
  (BUG-3293).
- **A failure prints its own lines.** `scripts/go-test-summary.sh` groups each
  failing test's output under its `--- FAIL`; `runCLI` bounds every call,
  reads the simulator's in-flight requests and goroutine profile past the
  bound, and never retries.
- **The Caddy HTTPS gateway** starts through `testutil/httpsgateway`, which
  returns once Caddy's log reports every managed certificate cached
  (`certificate obtained successfully` precedes caching); Caddy comes from a
  pinned release, since Ubuntu's is too old and Caddy's apt repository answers
  CI runners 402.

## Smaller lessons by service

- **Amazon ECS and AWS Cloud Map.** A Cloud Map DNS namespace owns a real
  Route 53 hosted zone and tasks resolve through the VPC resolver; the
  simulator writes no `/etc/hosts` block. Tests resolve the fully-qualified
  name, because an awsvpc task's search domain is not the namespace and a bare
  name does not resolve on AWS either. A test that deletes its subnet first
  stops its own cluster's tasks and waits with `aws ecs wait tasks-stopped`.
- **Amazon EC2.** A test acts on an instance only in the state the action
  requires: EC2 refuses to reboot a pending instance, and an instant committed
  sidecar image had hidden that race until the sidecar was built on the
  engine. The SDK suite's ECS fixture gives every VPC a /20 of its own out of
  400, since 25 /16s let two live VPCs share a range once more than 25 tests
  ran at once.
- **Amazon RDS.** The AWS CLI's waiter polls every 30 seconds, so CLI tests
  whose instance needs no backups create it with `--backup-retention-period
  0`. Amazon RDS events and Elastic Load Balancing's hosted zones and DNS names
  come from vendored guide pages (`scripts/fetch-aws-doc.sh` pins the branch
  an archived guide lives on, because its default branch deletes its pages).
- **AWS Lambda.** The CLI tests create functions with the architecture of the
  images `TestMain` builds, which match the host's. A reserved runtime
  variable in a function configuration is refused with Lambda's own message. A background
  worker starts the moment it is registered, so the event source pollers
  start after every slice is registered and the role-session stores open
  first of all: a mapping restored from an earlier process once polled
  through a role-credential store that did not exist yet and panicked.
- **Amazon CloudWatch.** `registerCloudWatchMetrics` makes CloudWatch's
  stores after the Amazon ECS, Elastic Load Balancing and Application Auto
  Scaling slices, so their metric publishers and the target-tracking
  evaluator start after every slice is registered: the ELB publisher's first
  flush, due at the next minute, once raced the store's creation in a
  simulator built just before the minute turned.
- **Amazon SNS.** A new topic carries the owner-only `__default_policy_ID`
  policy, and a topic's `DisplayName` defaults to empty, not to its name.
- **Google Cloud hosts.** Eventarc and Cloud Build both publish
  `/v1/projects/{p}/locations/{l}/triggers`, and the Host tells them apart;
  harnesses reach each through its own host with an HTTP proxy that delivers
  to the simulator. `bq` builds its client from BigQuery's Discovery document
  and tunnels any proxy, so a bare address serves `v2` as BigQuery's
  (BUG-3337). The gRPC door listens on the HTTP port plus two so the three
  simulators' defaults coexist.
- **gcloud.** The harness pins a release new enough to carry every command
  group the suite drives; an older pin lacked `run worker-pools` and failed on
  the client rather than the simulator. gcloud's worker-pool commands speak
  Cloud Run v2 over gRPC, which the simulator does not serve (BUG-3341), so the
  CLI suite deploys pools over REST.
- **Cloud Logging tails.** A tail streams only what is ingested after Cloud
  Logging reads its request, so tests open it with `openReadyTail`, which
  rewrites a marker until the tail streams it. Cloud Logging's own
  configuration calls write audit entries, so tests that read a whole bucket
  set those aside.
- **BigQuery.** Queries run in an in-memory SQLite copy of the tables they
  name, with GoogleSQL rewritten onto SQLite and functions registered on the
  connection; a cgo-based GoogleSQL engine would break the pure-Go build
  (BUG-3477 holds the untranslated constructs). A SELECT item takes its type
  from what it computes, so a comparison is BOOL and an alias naming a source
  column no longer borrows its type.
- **Azure Event Hubs.** `TestEventHubsSDK_StartPositions` runs alone: it
  learns that its Latest receiver attached from azcore's process-wide log
  listener, whose link line names no event hub, so another test's link once
  read as its own.
- **Azure Cosmos DB emulator.** It needs most of a two-core runner for minutes,
  so it starts once from `TestMain`, its two oracle tests run serially before
  the parallel phase in a shard of their own, and `TestMain` removes it
  explicitly because `os.Exit` skips deferred removal.
- **Azure Private DNS.** Auto-registration is a reconcile over links and
  machines run after every relevant write, not a hook per event, and never
  replaces a record set someone created.
- **Container Apps.** A CLI test waits for a template change to provision
  before splitting traffic with `latest`, which resolves to the latest ready
  revision; splitting at once landed on the earlier revision on a slow runner.
  HTTP ingress that names no target port scans the replica's TCP ports, as
  Container Apps documents, and targets the one that accepts; none or several
  set no target. Containers receive the platform's own variables
  (`CONTAINER_APP_NAME`, `CONTAINER_APP_REVISION`, `CONTAINER_APP_PORT` and
  the rest, and `CONTAINER_APP_JOB_NAME` and `CONTAINER_APP_JOB_EXECUTION_NAME`
  in a job) rather than a `PORT` of 8080, and the job execution `exec` route,
  which no `Microsoft.App` API defines, is gone.
- **Lint.** From golangci-lint v2.15.0 errcheck counts a receive from an error
  channel that drops the value; proxies signal completion on a channel of
  their own instead.
- **The mux overlap scanner** once treated `{$}` as a wildcard; it anchors a
  pattern at its end and shadows nothing.
- **The test-network range gate** counts a function as placing when it sends
  the REST body keys (`vnetConfiguration`, `virtualNetworkSubnetId`) as well
  as the SDK fields; a raw-request test was invisible to it and took a range
  another test held.

## Continuous integration and dependencies

Jobs are held to fifteen minutes; a timeout kill reports as "cancelled", which
reads as infrastructure noise. Suites are sharded on measured cost with each
test in exactly one shard; a matrix entry with a `placement:` is a deliberate
second run. A push to `main` whose tree passed on its pull request skips the
suites, because re-running every job per merge starved the account's runners.

Nothing CI builds or runs pulls from Docker Hub: Docker's official images come
from `mirror.gcr.io`, which serves them anonymously without Docker Hub's rate
limit. The ECR Public Gallery's copy failed every container test with `Data
limit exceeded` under load, and a copy in a registry the project owns would
have made every pin update a refresh; the gallery still serves what AWS itself
publishes there (Lambda base images, DynamoDB Local, CodeBuild images). Base
images are read out of the source and fetched once per run by one job,
because a cache miss in every job at once tripped per-minute throttles.
`docker load` restores no repository digest, so a digest-pinned image is saved
under the tag that spells its digest and found there.

Release pull requests run no CI and the owner merges them. release-please tags
the release pull request's merge commit; the Actions token cannot create a
ref at a commit whose workflow files differ from the branch heads, so a
workflow-changing merge landing before the release run stranded 2.4.1
(BUG-3465). A merged release pull request left labelled `autorelease: pending`
holds release-please on it for every later run; removing the label freed it,
and release-please computed v3.0.0 from main's own history although the v2.4.0
tag's commit is not on main. The release builds for darwin and from the
Dockerfiles, which no pull request job did, so `check-installable-build.sh`
builds the platforms it is given and checks each Dockerfile's workspace;
`build-gates` builds the runner's platform and one `release platform build`
job each of the other three, since a cold cross-compile of the three
simulators outruns the build gates' budget.
Publishes are keyed per commit and never cancelled; retention
spares anything under two hours old, because a publish between its two
per-architecture pushes looks abandoned. `actions/setup-go` went because the
runner fetched it once per job from codeload with no retry; a local action
downloads Go from the module proxy with retries.

A CI cache needs a fallback key. The lint shards restored only an exact
match, so every pin or dependency move linted `simulator-aws` cold, and a
runner lost mid-lint saved nothing; each shard now keeps its own Go and
golangci-lint cache and restores the newest older entry. On a cold cache,
`go vet` and golangci-lint's package loading compile `simulator-aws`'s one
package and its test variant, each near 7 GB, and the Go command runs them
side by side: 14 GB, and the 16 GB runner went down mid-lint, which GitHub
reports as a runner shutdown. `simulator-aws/Makefile` lints with
`GOFLAGS=-p=1`, one compile at a time, which peaks under 10 GB, and the
pre-commit lint hook reads the same `LINT_ENV`; a soft `GOMEMLIMIT` held
12 GB and ran slower. A document part that needs a few containers names them with
`base_images_match` in both the docs and warm-base-images matrices: the
contributing tutorial loads 2 of `simulator-aws`'s 37 images, since loading
all of them left its tests no room in the job's 15 minutes. The browser jobs
install `passt` from the Ubuntu archive pool, checked against the signed
index's SHA-256, and lift Ubuntu 24.04's AppArmor restriction on
unprivileged user namespaces, which the runner image leaves in force.

Dependencies of every class are held to their newest release past a 24-hour
quarantine; `ui/bunfig.toml` sets the same minimum release age, because caret
ranges let the resolver pick what the quarantine refuses. The check fails when
the proxy refuses a version listing: it once skipped such a module as current
and, on a full disk, reported everything current. Its self-test serves every
registry from fixtures, and reading `GOPROXY` runs with `GOTOOLCHAIN=local`.
`scripts/check-repo-config-sane.sh` exists because a `git config` run without
`-C` in a linked worktree wrote a test fixture's identity into the shared
`.git/config`.
