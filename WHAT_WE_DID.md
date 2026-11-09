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

- **`EnsureEngineBuild`**: a build holds a file lock named for its tag across
  processes as well as a lock within one. Two test binaries sharing an engine
  had built the sidecar's tag at once, and while the engine named the second
  result, the first found no image to create its container from.
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
- **Two APIs that publish one path are told apart by host.** Spanner's REST
  routes had lived under a `/spanner/v1/` the real service does not have,
  because Cloud SQL Admin publishes the same `/v1/projects/{p}/instances`.
  They are registered on `spanner.googleapis.com` instead, and the IAM gate
  and the audit log resolve Spanner's methods only for a call whose host names
  it. A client reaches that host through the simulator as its HTTP proxy, or
  at `spanner.<the simulator's host name>` under `.localhost`, the simulator's
  universe domain. The console reaches Spanner there, cross-origin, so the
  simulator answers CORS on a service host as Google's front end does, from a
  capture of that front end's answer. Every check that asks the route table
  whether a method publishes a call, the bearer check first among them, sees
  the canonical host too: one that saw the request's own host found no route
  for `spanner.localhost` and let it through without a credential.
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
- Firecracker creates its API socket file at bind and accepts connections
  only once it listens, so the agent waits for the socket to accept a
  connection, not for the file: a restarted guest whose first request landed
  in between failed with `connection refused`.
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

A container takes the route its subnet names as a guest does. A workload with
a gateway joins a holder sidecar that owns its namespace, and a one-shot
`sidecar route` holding `NET_ADMIN` alone sets or deletes the default route
there before the workload starts, so the first packet takes the subnet's
route; a later route table change reruns it in the same namespace. A kept
workload (an instance that stops on its disk) joins a kept holder that
outlives its stops and goes with its removal, because a route set after the
start raced the startup script past the NAT. A subnet whose table names no
0.0.0.0/0 route, or a target with no data plane, leaves the workload with no
route out, as does a Compute Engine instance with neither an external address
nor a Cloud NAT; the Amazon ECS test helper's VPCs carry an internet gateway
route, as a public subnet's tasks need to reach the simulator. The NAT
data-plane tests of all three clouds moved off the guest-only shards.
What a workload sends through a gateway reaches the simulator from the
gateway's sidecar, at its private address; the simulator attributes it to the
gateway's public address, as the cloud sees it: IAM's `aws:SourceIp`, Cloud
Audit Logs' `callerIp` and the Activity Log's `clientIpAddress`. Writing the
test found `DescribeNatGateways` reading only its first ID and answering an
unknown one with an empty list, which left the SDK's `NatGatewayDeleted`
waiter waiting out its whole budget; it answers `NatGatewayNotFound`.

A workload reaches its cloud's metadata at the address the cloud serves it
on. Each network sidecar answers at `169.254.169.254`, and on AWS at
`169.254.170.2` too: a one-shot `sidecar address` holding `NET_ADMIN` adds
them to its loopback. Each workload routes those addresses to the sidecar, a
container in the namespace it joins before it starts and a microVM guest from
its init before its first process. The sim-only variables went with it:
`AWS_EC2_METADATA_SERVICE_ENDPOINT`, `GCE_METADATA_HOST` and its siblings, and
`AZURE_INSTANCE_METADATA_ENDPOINT`; container credentials take the Amazon ECS
agent's `AWS_CONTAINER_CREDENTIALS_RELATIVE_URI` in place of a loopback
forwarder, and `metadata.google.internal` resolves to `169.254.169.254`.
Google Cloud's metadata server knows a caller by the container at its address,
which is now the holder a workload joined, so the lookup follows the holder's
`placement.ForwardedLabel` back to the workload.

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
A poweroff, the guest's or the stop's SIGTERM, sends SIGTERM to every other
process of the instance and waits for them to exit before the init does, as a
Linux init does, with the stop's grace period as the hard power-off. The init
had exited at once, which killed every guest process with it: a guest
approving an Azure Scheduled Event raced the operation it approved and could
lose the answer to it, even though it ignored SIGTERM. The approval's answer
also carries `Content-Length: 0`, so it is complete before the operation starts;
a body of unknown length ended only when the handler returned, after the
approval.
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
The attested document's `signature` is a PKCS#7 SignedData laid out as the
page's example decodes: the document attached, the signing certificate
inside, one SignerInfo without signed attributes. The certificate names the
machine's Region's subdomain of `metadata.azure.com` and `metadata.azure.com`,
the names the page tells a verifier to check, and is issued from the
simulator's persisted signing key with fixed fields, so a client trusts it
once.
A virtual machine resolves through Azure's wire server, `168.63.129.16`, as
the vendored page says a machine without a custom DNS server does: the
network sidecar holds that address beside `169.254.169.254`, and the machine
routes both to it. The sidecar answers each UDP query from the address it was
sent to; bound to every address, it had answered from its own, which a stub
resolver's connected socket drops.

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

Cloud Run's regional CPU quota was once a sliding window of CPU-minutes each
deploy debited, sized by two simulator variables that Google Cloud does not
have. It became what `cloudrun-quotas.md.gz` describes: the milli vCPU every
running instance of a service, function, job task or worker pool holds in its
project and region, from start to stop, against the value Cloud Quotas holds
in effect for `run.googleapis.com`'s `CpuAllocPerProjectRegion`, set through
the Cloud Quotas API's quota preferences (v1 and v1beta, gated and audited
from `cloudquotas-audit-logging.md.gz`). A deleted or unrouted revision's
instances stop holding CPU when the change completes, not when their
containers finish stopping, so a client that deletes and redeploys sees the
room at once. Cloud Quotas is absent from the public Discovery directory, so
the directory capture names it as unlisted.

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
interface at `RunTask`, so DeleteSubnet refuses while it holds it. A
container's metadata endpoint waits only for that container's placement,
because a container that read it at once found a 404 and a task-wide wait
would hold a running setup container until the container waiting on it
started. Express Mode and gateways build their load
balancers, certificates and scaling through the services' APIs as the
infrastructure role; writing straight into another service's store skipped
that service's rules.

A task's containers start in their `dependsOn` order, as the Amazon ECS agent
starts them: each waits until every dependency has started (`START`), exited
(`COMPLETE`), exited 0 (`SUCCESS`) or passed its health check (`HEALTHY`). The
dependency's own `startTimeout`, counted from its start, bounds the three
conditions after `START`, and no timeout applies when it is unset, since the
developer guide bounds the wait only when one is specified. A `SUCCESS`
dependency that exits non-zero, or one whose timeout runs out, stops the task
without starting the container waiting on it. The task stays PENDING, with the
waiting container PENDING, until every container has started or exited,
because the agent reports a task at its containers' earliest state. A
non-essential container that exits stops alone with its own exit code; an
essential one stops the task. Health checks run on the definition's interval
through the workload's exec, with Docker's retries and start-period rules,
and set `healthStatus` on the container and, from the essential containers
that declare one, on the task. In awsvpc mode the first container placed owns
the task's network namespace; once every container in it has exited, the
next one placed takes the task's interface in a new namespace, because a
container cannot join the namespace of one that is gone.

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
endpoints it serves. Azure Cache for Redis serves its `hostName` on TLS
6380, and on plaintext 6379 when enabled, to either access key.

Cache data moves through the engine's own formats. An ElastiCache snapshot
is each shard primary's RDB file, restored by restarting the primary from
it or importing it into a cluster; online migration follows the source with
`REPLICAOF`, or a host-side PSYNC link where Redis Cluster refuses
`REPLICAOF`, and cuts over by pausing writes until each link reaches the
source's replication offset. Azure Cache for Redis export writes the RDB to
a page blob through the Blob data plane as the cache itself, with the URL's
SAS or the cache's managed identity, so storage authorizes it like any
client. ElastiCache users become Redis ACL users, so the plane manages nodes
as an admin user of its own: a user group may switch `default` off without
locking the plane out. Redis `WAIT` counts acknowledgements of the
writes made on its own connection, so a test that writes and then waits for
a replica does both on one connection: `Plane.Command` dials afresh per call,
and a `WAIT` sent through it returned at once while the replica still lacked
the write.

A cluster's nodes listen on the port its node endpoints serve, so the
redirections and topology a node answers name that port. Redis takes
`cluster-announce-hostname` only from 7.0 and refuses it as a startup
directive before, so a node sets it with `CONFIG SET` once it runs; on an
older engine, which names its nodes' network addresses, the endpoints parse
each reply and rewrite the addresses in a `MOVED` or `ASK` redirection and in
the `CLUSTER SLOTS`, `SHARDS`, `NODES` and `REPLICAS` answers, pairing replies
with commands until a connection subscribes. A cluster forms with `CLUSTER
ADDSLOTS`, which every version has. An upgrade rolls through the nodes:
replicas restart on the new image, an upgraded replica takes over, and the old
primary restarts as a replica without first following a primary whose
snapshot format it cannot read; only a primary without a replica restarts in
place. An identifier serving several ports takes the first loopback address
on which all of them are free. The credential hook receives the AUTH
username and the client's address, and a grant with a lifetime closes the
connection unless a later credential extends it: an ElastiCache IAM
connection lasts 12 hours, the token's user must be the AUTH username, and
the client's address is `aws:SourceIp`. A Memcached plane dumps its items
with `lru_crawler metadump` and `get`, which backs Serverless Memcached
snapshots, and answers a binary-protocol `config get` (opcode `0x60`, as the
vendored ElastiCache Cluster Client source frames it) in turn with the
node's replies. `CopyFrom` copies each database of an instance source into
the database of the same number.

ElastiCache reports a change in flight as the resource's
`PendingModifiedValues`, or a user group's `PendingChanges`, until it lands.
Tagging answers the faults the model lists: `InvalidARN`, the not-found
fault of the ARN's resource type, `TagQuotaPerResourceExceeded` past 50,
`TagNotFound`, and the state faults while a replication group or serverless
cache is not available. Errors go out under the model's `awsQueryError`
code, which is how an SDK picks the typed error; several ElastiCache and
Amazon RDS refusals had used the shape name. A create records what it
restores from, so a simulator restarted mid-create loads it again, and
recovery settles snapshots before resuming transitions; a syncing serverless
migration starts its full synchronisation over on a cleared target. That
recovery runs once every slice is registered: an engine start needs the
Route 53 Resolver's network sidecars, and `awsNetworkSidecars` had cached the
"not running" answer a call made before the resolver started, failing every
later start in the process. Azure
Cache for Redis loads every import file in a scratch `redis-server` before it
stops the cache's engine, so a file that fails to load leaves the cache
serving; its identity resolves through `SystemAssignedIdentities_GetByScope`;
and its operations catalog takes the descriptions of the vendored
`rbac-permissions-databases` page. With SAS authentication an import or export
refuses a storage account outside the cache's subscription, an import one
outside the cache's region, and an export a token without the `racwdtm`
permissions the import and export guide lists; managed identity reaches
another subscription, as the guide allows.

Amazon ElastiCache keeps the events it logs: `DescribeEvents` reads them by
source type, identifier and time window (the last hour by default, 14 days
kept), newest first with a `Marker`, where it had invented one "created"
event per resource stamped with the time of the call. Creation, snapshots and
every migration transition log one. `PrepareMigration` answers `preparing`
and validates afterwards, as the online migration guide describes: a failure
logs an event naming the reason and removes the migration. ElastiCache reads
the migration secret first with the caller's credentials and then as
`elasticache.amazonaws.com`, so the secret's resource policy and its key's
policy must admit the service principal, at prepare and again at start.
Until cutover the cache's endpoint serves from the source: `sim/cacheengine`
diverts the endpoint's connections, sends each command to the source node
that owns its key, follows the source's redirections and answers the
topology from the cache, and the cutover ends the diversion while the
source's writers are still paused. A cluster-mode replication group target
takes the source's slot layout before it syncs, refuses writes through its
endpoints with the read-only replica error until the migration completes
(a refusal also closes the connections relayed without a guard, so each
client reconnects into one), and replays each propagated `MULTI` block as
one transaction on a connection it keeps open. Export to Amazon S3 and
seeding from it check the bucket the way ElastiCache does: through a bucket
policy for `<region>.elasticache-snapshot.amazonaws.com` or an ACL grant to
ElastiCache's canonical user. That check found that `PutBucketAcl` and
`PutObjectAcl` ignored the `x-amz-grant-*` headers; they set exactly the
grantees the headers name.

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
- **A Cloud SQL instance action acts on the engine or refuses.** `restart`,
  `failover`, the storage shrink pair, `truncateLog` and the PostgreSQL
  `preCheckMajorVersionUpgrade` run against the instance's engine and volume;
  a replica action on an instance that cannot be a replica refuses as Cloud
  SQL does. A restart of the simulator finishes, with an error, any operation
  it left RUNNING, since a RUNNING operation refuses every later one on the
  instance.
- **A Cloud SQL `databaseVersion` runs its own release.** The version table
  (`simulator-gcp/sqladmin_versions.go`) maps every version of the vendored
  enum that has a community image to that release by its release tag, as
  Amazon RDS's version table does (the dependency freshness gate keeps one
  moving digest per major series, which frozen minors such as MySQL 8.0.30
  cannot meet), and `databaseInstalledVersion` names the minor it runs; a MySQL
  8.0 version that carries its minor runs that minor. An earlier simulator ran
  PostgreSQL 16 and MySQL 8.0 for every version, so a restart records the
  version whose engine wrote an existing volume. Engine statements follow the
  release (MySQL 5.6 has no `CREATE USER IF NOT EXISTS`, PostgreSQL 12 no
  `DROP DATABASE ... WITH (FORCE)`, MySQL 8.4 no
  `--default-authentication-plugin`). `instances.patch` and `instances.update`
  with a later version run Cloud SQL's in-place upgrade as a
  `MAJOR_VERSION_UPGRADE` operation, with its labelled pre- and post-upgrade
  backup runs: MySQL's server upgrades the volume (`mysql_upgrade` before
  8.0.16), and PostgreSQL's data moves through `pg_dumpall` into a fresh
  cluster of the target; a failure restores the pre-upgrade backup. CI warms
  only the engine images the suites name, resolved from the table by
  `scripts/cloudsql-version-images-for.sh`, as the AWS Lambda runtimes are.
  An instance nobody has connected to has started no engine, so its upgrade
  swaps the release without carrying data or needing an admin credential.
- **Cloud SQL certificates are real X.509 material.** Each instance holds a
  per-instance CA rotation set, or a server certificate signed by the region's
  shared subordinate CA, and the data plane presents the active one;
  `serverCaCert` follows the instance's `serverCaMode` through patch and
  update. Export and import run the engine's own tools inside its container
  and move the bytes through the Cloud Storage API as the instance's service
  account, so the bucket's policy decides; `executeSql` reads its password
  from Secret Manager with the caller's credential.

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
- Amazon S3 serves a general-purpose bucket virtual-hosted style, as the
  SDKs address it: `<bucket>.s3.<host>` under any S3 endpoint host, and
  `<bucket>.<endpoint host>` for a client given a bare endpoint URL, where a
  name in front of `localhost` counts as a bucket only when the request is
  signed for `s3`, since other services share that family; no suite sets
  `UsePathStyle` or `s3_use_path_style` any more. The HTTPS gateway serves
  `*.localhost` and `*.aws.sockerless.localhost` too: without them the TLS
  handshake for a bucket's virtual-hosted name failed, and a Terraform
  apply retried the bucket's creation until the job's limit. The rewrite keeps a key's
  trailing slash (`photos/` is a key of its own), which the zonal and access
  point rewrites had trimmed. CreateBucket answers by the Region the request
  was signed for and the caller's account, and applies the vendored general
  purpose naming rules; a create is not a cross-account request, so another
  account's taken name answers `BucketAlreadyExists`, not `AccessDenied`;
- Amazon S3 validates the additional checksum an upload states, in a header
  or in the aws-chunked trailer, refuses a mismatch with `BadDigest` before
  anything is stored, keeps CRC32, CRC32C, CRC64NVME, SHA-1 or SHA-256 with
  the object (CRC64NVME when the upload names none) and returns it with
  `ChecksumMode`; a multipart upload keeps each part's and completes with a
  full object or composite checksum. A signed streaming upload's chunks and
  trailer are verified as they are read, each signature chained from the
  previous one as AWS's own signing library builds them, since AWS removed
  the S3 SigV4 pages;
- the Google Cloud gRPC listener verifies every call's bearer, over a TLS
  front end because Google's clients send credentials only over TLS;
- Google Cloud accepts a service account's self-signed JWT, as google-auth
  sends by default, only at the API whose service URL is its audience, and
  names each refusal in an `ErrorInfo` with the `google.api.ErrorReason`
  (`CREDENTIALS_MISSING`, `ACCESS_TOKEN_EXPIRED`,
  `ACCESS_TOKEN_TYPE_UNSUPPORTED`) and the service and RPC the call reached;
- Microsoft Graph requires a token with Graph's audience, and the password
  grant checks a verifier of the password Graph stored.

Each shortcut these replaced let a test pass without the credential its
client sends against the cloud.

Amazon Cognito user pools are an identity provider in their own right, the
one an Application Load Balancer's `authenticate-cognito` action names. The
control plane keeps pools, app clients, prefix domains and users; a password
is stored as a PBKDF2 hash and checked against the pool's policy. The prefix
domain `<prefix>.auth.<region>.amazoncognito.com` serves managed login and
the OAuth 2.0 endpoints the developer guide documents: `/oauth2/authorize`
sends a user without a session to `/login`, which sets the one-hour session
cookie and redirects with a single-use, five-minute code; `/oauth2/token`
redeems it with `client_secret_basic` or `client_secret_post` and PKCE,
refreshes, and grants client credentials; `/oauth2/userInfo` answers the
attributes the access token's scopes release; `/logout` ends the session. Each
pool signs RS256 tokens with its own key, which the issuer
`https://cognito-idp.<region>.amazonaws.com/<pool id>` publishes as its JWKS,
so a relying party verifies a token the way it does against AWS. The Smithy
model's enum values are the wire spelling: token validity units travel in
lower case although their member names are upper case.

The API signs users in as well: `InitiateAuth` and `AdminInitiateAuth` run
the password flows a client's `ExplicitAuthFlows` allow, check the client's
secret hash, and answer a user with a temporary password with the
`NEW_PASSWORD_REQUIRED` challenge, which `RespondToAuthChallenge` completes
under the pool's password policy. The operations the model gives no auth
scheme pass the SigV4 and IAM gates unsigned.

An Application Load Balancer's `authenticate-oidc` and `authenticate-cognito`
actions run on HTTPS listeners as the authentication guide describes. A
request without a session goes to the IdP's authorization endpoint with
`redirect_uri` `https://<host>/oauth2/idpresponse` and a state the load
balancer seals with its own key; the callback must arrive within 15 minutes
and present the `AWSALBAuthNonce` cookie, then the load balancer redeems the
code, reads the user info endpoint and sets the encrypted session cookie,
sharded `-0`, `-1`... at 4K, with a 7-day expiry and the action's
`SessionTimeout` sealed inside. Targets receive `x-amzn-oidc-accesstoken`,
`x-amzn-oidc-identity` and `x-amzn-oidc-data`, an ES256 JWT with padding
whose key `public-keys.auth.elb.<region>.amazonaws.com/<kid>` serves; the
load balancer replaces whatever claims headers a client sent. `deny` answers
401 to a request without a session, `allow` forwards it without claims, and
an expired access token is refreshed with the refresh token. An
authenticate-cognito action reads its app client's secret when it is
configured, through `DescribeUserPoolClient` signed as the caller, which is
the permission the guide says the caller needs; the load balancer reaches a
user pool's endpoints through the simulator's own handler chain and any
other IdP over the network.

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

An AWS STS session carries its limits with it. Each credential records its
identity policies, the permissions boundary of the user or role behind it and
the session policies its request passed, and the gate evaluates them in the
order the IAM User Guide's policy evaluation logic sets: an explicit deny
anywhere refuses, a resource policy naming the session itself grants past an
implicit deny, and otherwise identity or resource allow must also pass the
boundary and the session policies. `GetFederationToken` sessions intersect
the federating user's policies with the passed policy, and `AssumeRoot`
sessions carry the root-task policy. Durations follow the role's
`MaxSessionDuration` and the one-hour chaining cap, and MFA codes verify as
RFC 6238 codes from the virtual device's seed, so tests compute real codes. A device
remembers its last accepted time step and refuses a replayed code.
`GetSessionToken` sessions call IAM only with MFA and AWS STS only for
`AssumeRole` and `GetCallerIdentity`, and `AssumeRoot` runs only from the
management account or a delegated administrator, with root sessions enabled,
against an active member account. `aws:PrincipalArn` names the role behind a
role session, and a resource policy naming an IAM user matches that user's
federated sessions within their limits.

AWS IAM hosts every account of the organization. A request acts in the
account of the credential that signed it: names key roles, users, groups and
their policies per account, the management account keeps the original
tables, and each member account opens its own on first use. `CreateAccount`
creates `OrganizationAccountAccessRole` trusting the management account, and
a cross-account call passes only when both the caller's policies and the
target's trust or resource policy allow it. An access key with no user
belongs to the account's root user, which only explicit denies and service
control policies limit; the simulator seeds the management account's root
user the key `root`/`root`, as AWS issues the first one from the console, and the root-credential `AssumeRoot` tasks act on the
member root's real login profile, keys, certificates and MFA devices. Service
control policies govern every member-account principal from the root through
each OU down to the account, sparing the management account and
service-linked roles, and AWS Organizations attaches `FullAWSAccess` wherever
AWS does. AWS STS keeps a session's `SourceIdentity` from the request, SAML
attribute or web-identity claim, checks `sts:SetSourceIdentity`, and carries
it unchanged through role chaining and into `AssumeRoot`.

AWS Organizations holds every operation to the callers its model names: the
management account, a delegated administrator for the read-only operations
the User Guide lists, a member account the organization's resource-based
delegation policy allows for the delegable ones, any account in the
organization, or (for the operations that declare no
`AWSOrganizationsNotInUseException`) an account in no organization. A table
keyed on every operation carries the rule, and the simulator refuses to start
when an operation lacks one. A refused member gets `AccessDeniedException`.
`RemoveAccountFromOrganization` and a member's `LeaveOrganization` keep the
account as a standalone account whose principals still act and which no
service control policy governs. Access-denied messages name the resource the
request named and the policy kind that decided, worded as the IAM
troubleshooting page words it. An Amazon EC2 `UnauthorizedOperation` carries
an encoded authorization message, an opaque handle on the refusal's decision,
principal, action, resource and condition values that only AWS STS
`DecodeAuthorizationMessage` opens, for a caller allowed
`sts:DecodeAuthorizationMessage`.
A handshake answers only its parties: the invited account accepts or declines
an invitation, which reaches it by account ID or by its owner's email address,
the management account cancels it, and an account that is no party finds no
such handshake. Accepting an invitation joins the standalone account to the
organization's root under `FullAWSAccess` with `JoinedMethod` `INVITED`. An
unanswered handshake expires after 15 days, a closed one leaves the listings
30 days later, and AWS Organizations, not the cross-account rule, admits a
handshake's parties to it.
`EnableAllFeatures` runs the User Guide's standard migration. It refuses an
organization that has all features or a migration under way, cancels the
invitations still open, and sends every invited account an
`APPROVE_ALL_FEATURES` handshake under a `PARENT_HANDSHAKE` resource. The
management account's `ENABLE_ALL_FEATURES` request opens once the last of
them approves, or at once when no account was invited, and accepting it
enables all features. The attempt lasts 90 days; when it expires or closes
another way, the approvals still open are canceled. An organization created
with consolidated billing alone has no service control policies, and
`EnablePolicyType` refuses one with `PolicyTypeNotAvailableForOrganizationException`
until all features are enabled. Every AWS Organizations list operation pages
by `MaxResults`, at most 20, and `NextToken`, answering `INVALID_NEXT_TOKEN`
for a token it never issued, and `ListCreateAccountStatus` filters by
`States`.
The documentation jobs free the runner's unused preinstalled SDKs on a
runner with less than 20 GB free before they unpack the base-image archive,
as the simulator jobs do.
The `install-bun` action and `scripts/install-caddy.sh` retry their GitHub
release downloads through curl's doubling backoff on every error, riding out
minutes of 504 answers.
Every CI download through curl aborts a transfer that moves under 1 KiB/s
for 30 seconds and retries it, so a stalled mirror costs a retry, not the
job's time limit.

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
Where a reference page states nothing, the gate takes the service's own
method table: Cloud SQL's backups take the `cloudsql.backupRuns` permissions
and its flag and tier catalogs none, per its IAM permissions page, and
Firestore's streamed `write` and `executePipeline` take the entity permissions
of the writes a request carries and of the stages a pipeline runs.

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

Cloud KMS signs with the digest each key's algorithm names and refuses a
digest of another type, signs raw data for the algorithms that take it, and
backs Ed25519 keys with Ed25519. ML-KEM-768 and ML-KEM-1024 keys decapsulate
through `crypto/mlkem`, and HPKE import jobs open their wrapped keys through
`crypto/hpke`. An algorithm the Go standard library cannot back answers
UNIMPLEMENTED: creating one had silently produced symmetric material. A
deleted key leaves a retired resource that holds its name, and Autokey
provisions the key ring and key a key handle names. A scheduled destroy comes
due its full `destroyScheduledDuration` after the request, recorded to the
nanosecond: a destroy time truncated to the second had let a one-second schedule
fall due within milliseconds. Compute Engine and Secret
Manager `testIamPermissions` answer from the policy the IAM gate reads, where
two shared Compute helpers had echoed the request, and `getEffectiveFirewalls`
assembles the VPC rules and firewall policies that apply. Cloud Build refuses
a `secretEnv` no build secret defines, Cloud Logging drops two LogSink body
fields the API does not have, and Firestore keeps microsecond times over gRPC,
which also let update-time preconditions match.

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

The runtime spec validator judges every protocol the vendored models speak:
awsJson, awsQuery and ec2Query, restXml, and restJson1, whose operation it
finds within the model the request's SigV4 credential scope names, since the
REST JSON services share path shapes. Before it judged restJson1, an internal
`restApiIdRef` member rode every Amazon API Gateway response unseen.

A re-vendored model can name operations newer than the pinned SDK; such an
operation takes a scoped drift exemption citing its `BUGS.md` row, and a test
fails once the row closes or the operation is served.

## Testing discipline

- **A test waits on the state it asserts, from a workload that holds it.** An
  Azure Container Apps execution whose image ran its default command (`sh`
  with no input) ended in milliseconds, so a test that read `Running` after
  the start, or stopped the execution to read `Stopped`, raced the replica's
  exit. Those tests run a replica that lasts until the execution is stopped,
  under the 1800-second `replicaTimeout` Azure defaults to.
- **A document's commands are tested as written.** `testutil/doctest` runs
  every shell block of a tutorial in order, as a reader runs it, and checks the
  output the text promises. Its first runs found commands that had never
  worked: `@latest` resolved the simulators and `gcp-bootstrap-key` to a
  bootstrap `v0.1.0` the module proxy still serves; the Google Cloud stack
  deployed into a project only the Terraform suite's `TestMain` created; the
  contributing tutorial ran a microVM-only test. Every shell block in the
  repository carries an annotation, so a document gains no command nothing
  runs.
  A document job restores its cloud's simulator Go cache, which holds the
  simulator and its SDK and CLI suites; the shared toolchain entry held
  whatever the first job to save it built, and the contributing tutorial
  downloaded and compiled the AWS SDK suites cold inside its 13 minutes.
  A background block that exits before its ready line fails at once: its
  exit closes a channel rather than filling one, because the failed test's
  cleanup waits on the same exit, and a drained channel had held the README
  image job until the job's limit cancelled it.
  The README install and image parts load no base image: they install a
  simulator or run its release image and start no container, and loading
  every image on a slow runner had taken the Azure install part past 15
  minutes.
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
- **A harness holds the ports it hands out** (`testutil/heldports`): it binds
  each one, keeps the socket for the test's life, and gives it to every
  simulator and HTTPS gateway it starts on that port through systemd's
  socket-activation protocol, `LISTEN_PID` and `LISTEN_FDS` with the sockets
  from descriptor 3 on. `sim/listenfds` takes an inherited socket for a
  configured port it binds, matched by type and port as `sd_is_socket_inet`
  matches one: the API listener, the callback and diagnostics listeners, the
  DNS servers, the Google Cloud gRPC port and the Azure Service Bus AMQP port.
  Caddy takes them as `fd/3` and `fd/4`. Picking a free port, closing it and
  passing the number on had let another process take it first, and a
  simulator restarted on the same ports had raced every connection the suite
  opened while it was down.
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
- **Amazon API Gateway.** `CreateDomainName` mints an edge-optimized
  domain's Amazon CloudFront name or a Regional domain's `execute-api` name,
  with the Route 53 hosted zone the vendored `api-gateway-endpoints.md.gz`
  lists for the Region. It refuses an AWS Certificate Manager certificate that
  is missing, not `ISSUED`, or outside the Region its endpoint type reads
  from, so a Terraform stack takes the ARN from `aws_acm_certificate_validation`.
  `CreateVpcLink` answers `PENDING` and resolves to `AVAILABLE` once every
  target is a Network Load Balancer, or `FAILED`. `UpdateDomainName`,
  `UpdateVpcLink` and `CreateBasePathMapping` answer `NotFoundException` for a
  name nothing created.
  A REST API stage stores every field of the Smithy `Stage` shape, tags
  included, and `UpdateStage` accepts exactly the path and op pairs the
  vendored `api-gateway-patch-operations.md.gz` lists: it keeps a method
  setting under the key the path carries, as that page says the service does.
  Enabling a stage cache reports `CREATE_IN_PROGRESS` until the cluster
  settles at `AVAILABLE`. `CreateDeployment` with a stage name creates or
  repoints that stage, or with canary settings sets the stage's canary and
  leaves its deployment alone, and `DeleteDeployment` refuses a deployment a
  stage still uses. Every API Gateway identifier is ten lowercase letters and
  digits, never a truncated UUID with its hyphen.
  `GenerateClientCertificate` issues a self-signed X.509 client-authentication
  certificate whose common name is its identifier and whose validity ends at
  the `expirationDate` it reports; the private key stays in its own store,
  since no operation returns it.
  Both API versions serve one set of custom domain names: the v2 handlers
  read and write the v1 store and render a record as a v2
  `DomainNameConfiguration`, translating `RoutingMode` between the two enums,
  so a name either version creates conflicts with, reads through, tags
  through and deletes through the other. The v2 `CreateDomainName` checks its
  AWS Certificate Manager certificate as the v1 call does. The v2 API serves
  every operation of its Smithy model, `TagResource`, `UntagResource`,
  `UpdateDomainName`, `UpdateDeployment`, `UpdateModel` and `UpdateVpcLink`
  included.
  A base path mapping and an API mapping are one resource, as the vendored
  `api-gateway-rest-api-mappings.md.gz` describes: each version lists what the
  other created, a multi-level path comes only through the v2 API and only on
  a Regional domain with the `TLS_1_2` policy, a path is unique on its domain,
  and a domain in `ROUTING_RULE_ONLY` mode takes no mapping. Internal parent
  references (`restApiIdRef`, `methodRef`, `domainNameRef`, `apiIdRef`) are
  `json:"-"` fields the store persists and no response carries.
- **AWS Budgets.** An `RUN_SSM_DOCUMENTS` action stops what it names
  through the simulator's own services: `STOP_EC2_INSTANCES` halts the EC2
  instances, and `STOP_RDS_INSTANCES` stops the Amazon RDS DB instances in the
  action's Region with the same preconditions and `stopping` to `stopped`
  lifecycle `StopDBInstance` applies, so a cluster member, a read replica or
  an instance that is not available fails the execution.
- **Amazon EC2.** A test acts on an instance only in the state the action
  requires: EC2 refuses to reboot a pending instance, and an instant committed
  sidecar image had hidden that race until the sidecar was built on the
  engine. The SDK suite's ECS fixture gives every VPC a /20 of its own out of
  400, since 25 /16s let two live VPCs share a range once more than 25 tests
  ran at once.
- **Amazon EC2 Auto Scaling.** A group stores each tag's
  `PropagateAtLaunch`. It copies only the tags marked for launch to its
  instances and adds `aws:autoscaling:groupName`, which `AttachInstances`
  adds and `DetachInstances` removes, as the vendored
  `ec2-auto-scaling-tagging.md.gz` states. `DescribeTags` and
  `DescribeScalingActivities` honour the filters the `Filter` shape
  documents, and every Describe call refuses a filter name its operation does
  not list with `ValidationError`. A lifecycle hook's launch notification
  follows the instance's boot, so the SDK test long-polls the queue until the
  message arrives rather than for one 20-second window.
  An instance a group launches from a launch template carries the template's
  instance tag specifications merged with the group's propagated tags, the
  group's value winning on a shared key, as the vendored
  `ec2-auto-scaling-tagging.md.gz` states.
  A group stores its `InstanceMaintenancePolicy` within the ranges the model
  documents, clears it with `-1`, and lends it to an instance refresh whose
  preferences omit `MinHealthyPercentage` or `MaxHealthyPercentage`.
- **Elastic Load Balancing.** A Network Load Balancer holds
  `EnablePrefixForIpv6SourceNat`, `off` until set; the setting is `on` only on
  a dualstack load balancer, and a subnet mapping's `SourceNatIpv6Prefix` needs
  it on, as the model states. A load balancer reports the IPAM pool
  `CreateLoadBalancer` or `ModifyIpPools` gave it. A target group routes by its
  `load_balancing.algorithm.type` (round robin, least outstanding requests by
  each target's requests in progress, or weighted random) and holds a client
  with `lb_cookie` or `app_cookie` stickiness, as the vendored
  `alb-target-group-attributes.md.gz` describes: the load balancer generated
  cookie (`AWSALB`, or `AWSALBAPP-0` once the target sets its configured
  cookie) expires in 7 days and carries its issue time, against which the
  stickiness duration is enforced. `ModifyTargetGroupAttributes` refuses the
  combinations that page rules out, and a `stickiness.type` the model gives
  another kind of load balancer. A Network Load Balancer picks each TCP
  connection's target by the flow hash the vendored `nlb-introduction.md.gz`
  describes, over the protocol, both ends' addresses and ports and the TCP
  sequence number; no socket call reads the client's sequence number, so a
  fresh random number takes its place, which RFC 6528 makes equivalent. With
  `source_ip` stickiness it hashes the client's address alone, except behind
  a TLS listener. `sim/lbplane` hands the resolver each connection's `Flow`.
  A Gateway Load Balancer runs one node per load balancer and zone, a
  `sidecar gwlb` container on the load balancer's network at its address in
  the zone's subnet and on each Gateway Load Balancer endpoint's network at
  the endpoint's address, both held by network interfaces of the types the
  model names. A route naming the endpoint sends a subnet's traffic to that
  address; a policy rule routes what arrives there into a TUN device, and the
  node wraps each packet in GENEVE with the option class 0x0108 endpoint,
  attachment and flow-cookie options the vendored tunnel handler parses, to a
  healthy appliance picked by 5-tuple, `source_ip_dest_ip_proto` or
  `source_ip_dest_ip`, keeping a flow on its appliance unless failover says
  `rebalance`. What the appliance returns leaves through a second rule as the
  endpoint's subnet routes it. The node long-polls the simulator for its
  targets' health and the group's stickiness, and the health sweep wakes it.
  Its health checks come from its own addresses, which the appliances'
  security groups must admit. The node reconciler starts with the other
  workload starters once every slice is registered: started inside
  `registerELBv2`, it read the Amazon EC2 stores while a conformance test
  re-registered them. A Gateway Load Balancer endpoint is `Pending`
  until a node has applied state that names it, and its client waits for
  `Available` before routing through it: CreateVpcEndpoint had answered
  `Available` while the node serving it was still being replaced, and a
  client whose flows crossed the replacement lost a connection. Each node
  instance answers to its own name and loses its token before its container
  stops: the replaced node, still asking while it wound down, had made the new
  node's endpoints `Available` before the new node existed, which a slow CI
  runner exposed. A Gateway Load Balancer listener carries no `Port` or
  `Protocol`, before and after a restart: CreateListener had defaulted both to
  `HTTP` on port 80, which `aws_lb_listener` read back as drift.
  `sim.ContainerConfig.JoinNetworks` puts a
  container on further networks before it starts, so the node finds every
  endpoint's interface at its first instruction. Interface endpoints hold
  real network interfaces too, a route to an endpoint reads back under
  `GatewayId`, and `DescribeInternetGateways`, which had ignored its filters
  and every ID after the first, applies both.
  Each endpoint has its own TUN device: a rule per subnet whose route table
  sends its default route through the endpoint picks it by source, so two
  endpoints of one load balancer on one VPC network name the right one in the
  GENEVE options. Engine networks carry each cloud's documented MTU through
  `sim.CloudNetwork.MTU`: 9001 for an Amazon VPC, a Compute Engine network's
  own `mtu` (1460 unless created with another value from 1300 to 8896, which
  the network now stores and reads back) and 1500 for an Azure virtual
  network. The node drops packets over 8,500 bytes and sends GENEVE with
  fragmentation forbidden, so a full-sized packet crosses whole. The node
  turns its reverse-path filter off through `sim.ContainerConfig.Sysctls`:
  it inherited the host's, loose on Ubuntu, which dropped what an appliance
  returned through a TUN device that holds no address, so on GitHub's runners
  every connection through a node's second endpoint timed out.
- **NAT data planes under parallel changes.** Azure NAT Gateway and Cloud NAT
  reconcile one gateway at a time. A reconcile that a parallel virtual
  network or router change started found no sidecar in the map and removed,
  as stale, the container a machine's boot was still starting, which failed
  the boot with `AllocationFailed`.
  A sidecar that ends before it serves — an egress sidecar, a Gateway Load
  Balancer node, a share container — reports its last five lines in the
  error, so a failed boot names the cause even where the simulator passes
  the sidecar no log sink.
- **Route 53 and AWS Cloud Map.** `GetChange` answers `NoSuchChange` (404)
  for an ID no call returned, as the Smithy model declares; every change the
  simulator reports is stored. Cloud Map's `ListOperations` filters
  `UPDATE_DATE` with `BETWEEN` over Unix times and refuses any other
  condition with `InvalidInput`.
- **Amazon RDS.** The AWS CLI's waiter polls every 30 seconds, so CLI tests
  whose instance needs no backups create it with `--backup-retention-period
  0`. Amazon RDS events and Elastic Load Balancing's hosted zones and DNS names
  come from vendored guide pages (`scripts/fetch-aws-doc.sh` pins the branch
  an archived guide lives on, because its default branch deletes its pages).
- **AWS Amplify.** An app, branch, webhook, job and domain association carry
  every member the model marks required: an unset description, repository,
  display name, framework or active job reads back `""`, environment variables
  `{}`, custom domains `[]`, and a job with no commit reports its start as
  `commitTime`. Clearing a description through an update therefore reads back
  the empty string rather than dropping the member.
- **AWS Lambda.** The CLI tests create functions with the architecture of the
  images `TestMain` builds, which match the host's. A reserved runtime
  variable in a function configuration is refused with Lambda's own message. A background
  worker starts the moment it is registered, so the event source pollers
  start after every slice is registered and the role-session stores open
  first of all: a mapping restored from an earlier process once polled
  through a role-credential store that did not exist yet and panicked. `PublishVersion` and
  `ListVersionsByFunction` answer the function configuration shape, never the
  stored code location. An event source mapping keeps the version or alias
  qualifier its `FunctionName` names and invokes that version, as AWS does.
  Amazon EFS returns a file system's `Tags` as a list
  even when it has none, since the model requires the member.
- **Amazon EFS and AWS KMS.** `CreateFileSystem` keeps `Encrypted` and
  `KmsKeyId` and reports the key as its ARN. An encrypted file system created
  without a key uses the AWS managed key `aws/elasticfilesystem`, which
  `kmsAWSManagedKey` creates in the account the first time it is needed, with
  `KeyManager` `AWS`. A replication's new destination file system is encrypted
  the same way. `KmsKeyId` without `Encrypted` is refused, as is a key that is
  not symmetric. A creation token already in use answers
  `FileSystemAlreadyExists` with the existing file system's ID. `CreateAlias`
  refuses the `alias/aws/` prefix, which the model reserves for AWS managed
  keys, and a key reference may also be an alias ARN.
- **Amazon CloudWatch.** `registerCloudWatchMetrics` makes CloudWatch's
  stores after the Amazon ECS, Elastic Load Balancing and Application Auto
  Scaling slices, so their metric publishers and the target-tracking
  evaluator start after every slice is registered: the ELB publisher's first
  flush, due at the next minute, once raced the store's creation in a
  simulator built just before the minute turned.
- **Amazon CloudWatch alarm state.** `SetAlarmState` runs the new state's
  actions when the state changes, as the API reference says. A metric alarm
  keeps the set state only until its next evaluation, which returns it to its
  actual state and runs that state's actions; tests read the change from the
  history and the delivered notifications, and the Amazon ECS rollback test
  breaches its alarm with `PutMetricData` instead. A composite alarm parses
  its `AlarmRule` by the User Guide's grammar, `AT_LEAST` included, and the
  evaluator moves it to ALARM while the rule holds and OK otherwise, after
  the metric alarms it reads; a transition publishes the composite
  notification with `AlarmRule` and `TriggeringChildren` and the reason
  wording the alarm events show. A `SetAlarmState` on a composite alarm holds
  until a child's state changes or the alarm is updated.
  Every metric and composite alarm transition also reaches the default
  event bus as a "CloudWatch Alarm State Change" event shaped like the User
  Guide's examples, and an alarm with `AlarmTagsPropagation` enabled carries
  its included or non-excluded tags, never an `aws:` key, in that event and
  in its Amazon SNS notification.
  An action naming an AWS Lambda function invokes it asynchronously with the
  User Guide's alarm payload when the function grants
  `lambda.alarms.cloudwatch.amazonaws.com` `lambda:InvokeFunction`. Log
  alarms evaluate on their scheduled query's schedule, accept
  `SetAlarmState`, and run their actions and events as metric alarms do.
  Creating, updating and deleting any alarm sends a "CloudWatch Alarm
  Configuration Change" event, an update with its previous configuration,
  and an update leaves the alarm's state unchanged as the API reference
  says, where the simulator had reset it to INSUFFICIENT_DATA. A composite
  alarm returns its `StateReasonData` and its real state timestamp over all
  three protocols, and its query-protocol describe its `StateReason`.
- **Test fidelity: Amazon SNS, AWS Systems Manager, Amazon RDS.** Amazon SNS
  confirms an HTTP or email subscription with an opaque random token that only
  the confirmation message carries, where it had been the subscription ARN
  without its colons; the confirmation tests read it from a real endpoint.
  `PutParameter` records each version in the parameter's history as it writes
  it, and a deleted parameter drops its history, where the history had been
  reconciled on read and lost a version overwritten between two reads. The
  RDS snapshot restore test dials the restored endpoint whatever its host
  name, instead of branching on the simulator's.
- **Amazon SNS.** A new topic carries the owner-only `__default_policy_ID`
  policy, and a topic's `DisplayName` defaults to empty, not to its name.
- **Cloud Run image export and Firestore REST listen.** `exportImage` copies
  the image the revision, execution or service runs — from the simulator's
  Artifact Registry or over the Registry v2 API — into the Artifact Registry
  repository `destinationRepo` names, and `exportStatus` reports the exported
  digest or the failure. Firestore's REST `documents:listen` runs the gRPC
  watch for the one request its body carries and streams each ListenResponse
  as an element of a JSON array, so a client reads the target's documents,
  CURRENT, and each later change while the exchange stays open.
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
- **Azure networking reads.** A load balancer keeps the outbound rules its PUT
  carries, lists and reads them as children, and the instance metadata
  service reports their frontends to each backend. A virtual network's
  usages count each subnet's held addresses against the addresses Azure
  leaves to tenants, and `CheckIPAddressAvailability` answers a taken or
  platform-reserved address with the next free ones, from every subnet when
  the candidate lies outside them, which `az network vnet
  list-available-ips` relies on.
  A network interface's effective route table is its subnet's, as
  `virtual-networks-udr-overview` lays it out: a route per address range,
  Azure's None routes for the reserved ranges the address space does not
  cover, a route per peered range, the Internet default, and the route
  table's user routes, which turn a default route with the same prefix
  Invalid. Network Watcher's next hop picks from the same table by longest
  prefix and names "System Route" for a system route's table, as the
  specification says. Its effective security groups are the interface's and
  the subnet's, each with its association and its rules in priority order.
- **Network Watcher packet capture.** A capture runs where the Network
  Watcher agent extension would: once the machine has
  `NetworkWatcherAgentLinux`, a session places pcapagent in the machine's
  network namespace, where an `AF_PACKET` socket reads every frame crossing
  its interfaces into a libpcap file, through the 5-tuple filters (an entry, a
  range or a `;` list, matched from the machine's side), the per-packet
  truncation and the session's byte and time limits, with the defaults the
  specification declares. The create answers only once the agent's socket
  receives, so traffic the caller sends next is captured. When the session
  stops — on request, at its size, or at its time limit with `TimeExceeded`,
  as `network-watcher-packet-capture-manage` reports it — Network Watcher
  moves the file to `network-watcher-logs/subscriptions/…/packetcapture_{time}.cap`
  in the storage account with an account SAS the account's key signs, and to
  the machine's `/var/captures` path; a failed move reports `StorageFailed` or
  `LocalFileFailed`. Deleting the resource leaves the file. The capture had
  refused every machine with `PacketCaptureTargetNotReady` before.
- **Container Apps system log events.** A revision writes the messages
  `app-containerapps-logging` lists, with its Source and its `Info` or
  `Error` Type, as each step happens: `Creating a new revision`, `Successfully
  mounted volume` for each template volume, `Successfully provisioned
  revision` or `Error provisioning revision … ErrorCode` (`ContainerCrashing`
  for a replica whose container exited, `Timeout` for one never ready, and
  `ErrImagePull` for one whose container never ran), `Setting a traffic
  weight` for the revisions that take traffic, and `Deactivating old
  revisions since 'ActiveRevisionsMode=Single'`. A job execution had logged
  "Container started" when the execution record was written, before any
  container ran; each replica's container now reports its start once it
  runs, and tests wait for that row instead of reading the table at once.
- **Azure Cosmos DB containers own their documents.** A document read,
  write, query or patch against a container that does not exist answers 404
  `Owner resource does not exist`, as reading the container does; the data
  plane had stored documents under any database and container a path named.
  A `SELECT VALUE` query returns bare values (`[2]` for `SELECT VALUE
  COUNT(1)`); it had wrapped each in `{"$1": …}`, which the raw-HTTP query
  test asserted and `azcosmos` exposed once the test drove it.
- **Azure Storage data actions offer choices.** Where
  `storage-authorize-with-entra-id` names alternatives, a Microsoft Entra
  caller holding either is authorized: Put Blob, Put Blob from URL and Copy
  Blob create a blob with `blobs/add/action` but replace one only with
  `blobs/write`; Snapshot Blob and Append Block take either; Put Message takes
  `messages/add/action` or `messages/write`; Get Messages takes
  `process/action` or `delete` and `read` together; Delete Message takes
  `process/action` or `delete`; Insert Entity takes `entities/write` or
  `add/action`, Update and Merge Entity `write` or `update/action`, and the
  insert-or-replace forms `write` or `add/action` with `update/action`. Set
  Queue Service Properties needs `queueServices/read`, as the page lists it.
- **Azure suites wait on events.** The Cosmos DB TTL test reads the item
  until the service answers 404, the queue test waits out the visibility
  instant the dequeue reported, the cross-job DNS test drives
  `armappcontainers` and reads the lookup from the environment's console
  logs rather than execing into containers found by Docker labels, and the
  Cosmos DB query, upsert and patch tests drive `azcosmos`.
- **App Service catalogs and plan actions.** `geoRegions` lists the
  subscription's regions by display name, which `az appservice
  list-locations` matches against, and none for Xenon workers or custom-mode
  plans, which no region here runs. `restartSites` restarts every app and
  slot on the plan, which each site's restart detector then reports; a soft
  restart restarts nothing, since every configuration change applies as it is
  made. `MigrateMySql` and `MigrateStorage` had recorded migrations that moved
  nothing; they answer a declared 501.
- **App Service and Blob reads.** A site's `authsettings` and
  `authsettingsV2` lists answer what PUT stored, as the slot lists did, and
  the v1 settings carry the `configVersion` App Service fills in — `v2` once
  the site holds a v2 configuration, `v1` otherwise — which
  terraform-provider-azurerm requires before it reads `auth_settings` back;
  a setting PUT sends as an empty string reads back as null. A
  path-style Blob request may name an account `internal`; no surface claims
  the segment.
- **Log Analytics soft delete.** Deleting a workspace keeps it, its data, keys
  and tables for 14 days, as `log-analytics-delete-workspace` describes:
  `deletedWorkspaces` lists it, the retention sweep leaves its rows alone,
  and a PUT with its name, group and region recovers it with its customer ID.
  `force=true`, or the end of the 14 days, deletes it for good and releases
  the name. The group-scoped `deletedWorkspaces` route had been registered
  under `resourcegroups`, which the path rewrite never leaves, so it had
  answered 404.
- **Service Bus auto-forwarding.** A queue or subscription with `forwardTo`
  hands each message it receives to the queue or topic it names, and one with
  `forwardDeadLetteredMessagesTo` hands on its dead letters, as
  `service-bus-auto-forwarding` describes: the destination must exist when
  the source is created, a session-enabled entity cannot forward, the source
  accepts no receiver, and a message past four hops, bound for a missing or
  disabled destination, or without the session ID a session-enabled
  destination needs, is dead-lettered on the source.
- **Azure Container Registry webhooks, import and usage.** A push or manifest
  delete that a webhook's actions, scope and status select reaches its
  service URI as `container-registry-webhook-reference` lays out the event,
  with the request's host, method and user agent, which `sim.OCIRegistry`'s
  manifest hooks now carry; ping sends a ping event, and `listEvents`
  returns each exchange, newest first, with Authorization masked.
  `importImage` copies the source image, every manifest an index lists (at
  most 50) and its blobs byte for byte: from the simulator's own stores for
  a registry it serves, otherwise over the Registry v2 API through
  `sim.PullOCIImage`, which answers the bearer challenge with the given
  credentials. `listUsages` counts the stored bytes and the webhooks against
  the SKU's included storage and webhook count from `container-registry-skus`,
  and a webhook past that count is refused. terraform-provider-azurerm 5.13
  addresses webhooks as `webHooks`; the path normalizer folds that segment
  under a registry to the specification's `webhooks`.
- **Azure Cosmos DB metrics.** Every data-plane request leaves a sample with
  its container, status and charge, and every write a snapshot of its
  container's documents, kept 60 days. `Total Requests` and `Total Request
  Units` sum the samples in each grain, `Document Count`, `Data Usage` and
  `Index Usage` carry a container's last snapshot forward, `Service
  Availability` is the share of requests not answered 5xx, and the usages
  count what the containers hold now. A container's one partition keeps a
  stable partition ID.
- **Logic Apps run details.** The engine records a repetition of every action
  inside a For each loop per iteration, a scope repetition of the loop per
  iteration, and the request and response of each Http action, on the action
  or on its repetition; the run stores them, and the Microsoft.Logic and App
  Service workflow routes read them back. `items('loop')` names a loop's
  current item, which nested loops need.
- **PostgreSQL long-term backup.** `startLtrBackup` refuses a SAS URI that
  names no single container or carries no signature, and once the volume is
  captured writes it, as a tar archive `sim.ArchiveVolume` reads through a
  container that never starts, to a block blob in each container through the
  Blob service with that SAS; a refused write fails the backup.
- **Azure Storage endpoints.** The Files, Queue and Table data planes answer
  only at an account's own `<account>.<service>.` host; the `/file/`,
  `/queue/` and `/table/` path prefixes, which no Azure endpoint serves, are
  gone, and the Blob service keeps the IP-style `/<account>/` form the SDKs
  use for an endpoint addressed by IP. The CLI suite advertises
  `<account>.<service>.core.cli-shim.localhost`, the shape the CLI's storage
  SDK reads an account name from, and reaches it through `HTTP_PROXY`.
- **AWS Organizations.** `CreateGovCloudAccount` creates its commercial
  account through the same path as `CreateAccount`, with FullAWSAccess
  attached and the access role the request names, and `PutResourcePolicy`
  refuses a delegation policy with `NotAction` or `NotResource`, which AWS
  stopped accepting on June 30, 2026.
- **Amazon S3 ListBuckets.** A request that names `max-buckets`, `prefix`,
  `bucket-region` or `continuation-token` is paginated, as the model states:
  it pages at `max-buckets` (10,000 by default), sorted by name, with an
  opaque continuation token, and each bucket carries `BucketRegion`; an
  unpaginated request names no Region.
- **IAM policy documents** are URL-encoded RFC 3986-style, every byte but the
  unreserved characters as `%XX`, so the AWS CLI decodes a space as a space,
  not `+`.
- **Azure Compute images.** ComputeRP's `virtualMachineImage.json` and
  `image.json` are vendored, so the conformance probe checks the marketplace
  catalog and `Microsoft.Compute/images` routes instead of an allowlist; the
  edge-zone catalog operations declare a 501, since no Extended Zone runs.
  The 2024-07-01 documents are vendored beside them, and a machine image
  version carries `imageDeprecationStatus` from api-version 2022-11-01, the
  first that declares it.
- **Volume helper platform.** The disk copy helper and `sim`'s volume
  snapshot and archive helper ran the Alpine image as `linux/amd64` on every
  engine; they read the platform off the image the engine pulled, so an arm64
  engine runs them natively.
- **Azure managed disks.** DiskRP's `disk.json` (2023-04-02, the version
  `az disk` speaks) is vendored and `Microsoft.Compute/disks` served against
  it: Empty and Copy creates, grow-only resizes, `diskState` derived from the
  holding machine's power state (Unattached, Attached, Reserved), and a 409
  `OperationNotAllowed` for deleting a held disk. A disk's contents are an
  engine volume of its own. A machine's workload mounts a disk bay volume at
  `/dev/disk/azure/scsi1`, and an attached disk is the bay's `lun<N>`
  directory: attaching copies the disk in, detaching copies it back out, so a
  running guest sees disks come and go without a reboot and what it wrote
  follows the disk. `storageProfile.dataDisks` (Attach, Empty, Copy) and
  `attachDetachDataDisks` share one plan, so both paths validate LUNs and the
  size's data disk count alike. `az vm disk attach` and `az vm update` re-send
  the machine without `imageReference`, so a machine PUT keeps the
  creation-only `imageReference` and `osProfile` it was created with. DiskRP
  answers a write 202 and az reads the result from the Location monitor URL
  (final-state-via location), so a disk operation's result is the disk. A
  simulator without a state directory sweeps only its own run's machines at
  shutdown: the engine lists every simulator's, and one CLI test's shutdown
  had removed a concurrent test's running machine.
- **Azure Cosmos DB autoscale migration.** Migrating manual throughput to
  autoscale sets the maximum the autoscale FAQ states: the largest of 1000,
  the manual throughput, a tenth of the highest throughput the resource ever
  had and ten times its storage in GB, rounded to the nearest 1000. The
  simulator records each resource's peak throughput for that rule.
- **Scoped data directories.** With no data directory set,
  `sim.ScopedDataDir` gives each process its own temporary directory, which
  the last of the process's servers to stop removes, so two simulators, or
  two runs of one test, never share App Service site homes.
- **Cloud Logging sinks** honour `customWriterIdentity`, admitted only for a
  sink routing to a log bucket in another project, keep
  `outputVersionFormat`, and carry `createTime` and `updateTime`.
- **Cloud KMS import jobs** expire three days after creation, and a key ring
  that still holds an unexpired import job refuses deletion.
- **Compute Engine firewall policies.** Network firewall policies clone
  another policy's rules with `cloneRules`. Organization firewall policies
  take a numeric `name`, require `shortName` (gcloud's `displayName` stands
  in), record `parent`, and run their operations under
  `locations/global/operations` with an `org-` name and the policy's id as
  `targetId`, which the Terraform provider and gcloud's poller read.
- **Compute Engine IAM verbs.** `getIamPolicy`, `setIamPolicy` and
  `testIamPermissions` on every Compute Engine collection first serve the
  collection's own get method for the resource they name, and a resource
  never created receives that method's 404, so no policy lands on an absent
  resource. A unit test sends every mounted verb to an absent resource and
  expects the structured 404; the SDK suites create each resource before its
  policy verb, a gcloud test binds a policy on an image it created and is
  refused one on an image it never created, and a licence code's policy
  belongs to a code a licence in the project was issued.
- **Compute Engine instance identity.** An instance inserted without a
  service account has no identity on the metadata server, and identity tokens
  name the account by its IAM `uniqueId` in `sub` and `azp`.
- **Cloud Run** refuses a CPU limit outside the values `cloudrun-cpu.md.gz`
  lists, and its invoke check gives binding conditions `request.host` and
  `request.path`.
- **Cloud SQL `databases.list`** reads a running instance's engine catalog,
  so databases `executeSql` or an import created or dropped show as they are.
- **Service Usage.** Enabling `cloudapis.googleapis.com`, as
  `gcloud projects create` does, enables the default services
  `service-usage-enabled-service.md.gz` lists. Workforce pools default
  `sessionDuration` to `3600s`, and Cloud Scheduler's enums read through the
  shared `protoEnum` tables.
- **The gcloud CLI suite** clears an ambient `CLOUDSDK_AUTH_ACCESS_TOKEN` and
  `CLOUDSDK_PROXY_*`, which made gcloud bypass the activated account.
- **Firestore REST `listen`** is a server-streaming method to the spec
  validator, which reads its JSON array element by element.
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
- **Cloud Storage errors.** Every JSON API route answered the JSON API's
  own envelope, `{"error":{"code","message","errors":[{"domain","reason",
  "message"}]}}` with no `status` and no `details`, and took each reason from
  the vendored `storage-json-status-codes.md.gz`: `invalid`, `required`,
  `notFound`, `conflict`, `forbidden`, `conditionNotMet` and the rest by HTTP
  status. A request without a credential answered `required`/`Login Required`
  located at the `Authorization` header and one with a bad credential
  `authError`; the XML API answered `AuthenticationRequired` and
  `InvalidAuthentication` from `storage-xml-status-codes.md.gz`.
- **Cloud Storage object ACLs at creation.** An object took its ACL when it
  was written, as `storage-access-control-lists.md.gz` describes: the
  `predefinedAcl` or `destinationPredefinedAcl` the write named, else the
  `acl` its resource carried, else the bucket's default object ACL, and in
  every case its uploader, named in the object's `owner`, held `OWNER`. An
  overwrite was a new object with a new ACL and a new owner; a retired
  generation carried its ACL into the soft-deleted entry, which restore's
  `copySourceAcl` brought back. `objects.patch` took `predefinedAcl` too,
  which Terraform's `google_storage_object_acl` sends.
- **Cloud Dataflow reports only what ran.** No worker runs a job's steps, so
  `getMetrics` answered no metric updates, `messages.list` no messages,
  `getExecutionDetails` no stages, a stage's details and the debug methods
  `NOT_FOUND`; the fixed `ElementCount` of 0, the "Worker pool started."
  message, stage `s1` and `worker-0` went. `templates.launch` and
  `flexTemplates.launch` refused a launch without the job name Discovery marks
  required instead of naming the job themselves, and the CLI suite launched its
  job with `gcloud dataflow jobs run`.
- **Firestore timestamps over REST.** A REST write stored each
  `timestampValue`, nested ones included, in UTC rounded down to the
  microsecond, as the Value schema states, and refused one that is no RFC 3339
  time; equality and ordering compared timestamps as instants, so
  `03:04:05.123456Z` and `04:04:05.1234560+01:00` matched, and change streams
  stamped their times to the microsecond.
- **Firestore pipeline operations.** `executePipeline` over REST and gRPC ran
  the pipeline it was given against the stored documents. The stages
  (`collection`, `collection_group`, `database`, `documents`, `literals`,
  `where`, `select`, `add_fields`, `remove_fields`, `sort`, `limit`, `offset`,
  `distinct`, `aggregate`, `replace_with`, `union`, `unnest`, `sample`, `let`,
  `find_nearest`, and `subcollection` inside `array(...)`/`scalar(...)`) and
  the comparison, logical, arithmetic, string, array, map, reference, vector,
  timestamp, type and aggregate functions followed the vendored
  `firestore-pipeline-*.md.gz` pages; values ordered as `firestore-data-types.md.gz`
  states, and the wire form came from the Go client's `pipeline_stage.go`.
  A row carried the document's `__name__`, `__create_time__` and
  `__update_time__` as fields, so a `select(...)` that left them out answered
  a result with no name, as the API describes. `FSValue` held strings and
  bytes as pointers, which kept an empty string a string instead of letting
  `omitempty` turn it into a missing value.
- **Cloud Resource Manager purge.** A project shut down 30 days earlier was
  deleted for good by a background purger that woke when a project was
  deleted or restored and when the next recovery window closed, as
  `resource-manager-delete-restore-projects.md.gz` states. Its ID was
  tombstoned, so `projects.create` refused to reuse it as it refuses an ID in
  use (`resource-manager-creating-managing-projects.md.gz`), and a purged
  default project was not recreated at start.
- **AWS Systems Manager maintenance windows.** A window runs on its
  `Schedule`: the simulator's ticker reads the cron (with the optional seconds
  field), rate or at() expression in `ScheduleTimezone`, moves cron runs
  `ScheduleOffset` days later, keeps runs inside `StartDate` and `EndDate`,
  counts rate intervals from `StartDate`, and skips disabled windows. Each run
  records an execution that runs the tasks in priority order, one priority in
  parallel, starts no task after the window's end minus `Cutoff`, and records
  a run that meets the previous one in progress as `SKIPPED_OVERLAPPING`. A
  task calls its service through that service's API as Systems Manager, with
  the task's service role or `AWSServiceRoleForAmazonSSM`: `RUN_COMMAND` sends
  one command per target node and waits on the command store's writes,
  cancelling at the cutoff under `CANCEL_TASK`; `LAMBDA`, `STEP_FUNCTIONS`
  and `AUTOMATION` invoke the function, start the execution or the
  Automation. Execution, task and invocation reads report what ran, where they
  had reported one SUCCESS execution derived from the window ID;
  `NextExecutionTime`, `DescribeMaintenanceWindowSchedule` and
  `DescribeMaintenanceWindowsForTarget` read the schedule and the registered
  targets, and `SendCommand` resolves `tag:` and `tag-key` targets. The tests
  schedule an at() window seconds ahead and wait for its run.
- **AWS Systems Manager Automation.** `StartAutomationExecution` had
  recorded one settled `aws:runShellScript` step with status Success, so an
  execution and an `AUTOMATION` maintenance window task reported work nothing
  did. The execution became a run of the runbook, an Automation document of
  schema version 0.3 in YAML or JSON, taken from the version the request
  named. The start types each declared parameter from the request or its
  default (a `{{ssm:name}}` default read from Parameter Store as the caller)
  and refuses an undeclared, missing or disallowed value with
  `InvalidAutomationExecutionParametersException`; a document that is no
  Automation runbook answers `AutomationDefinitionNotFoundException`. A
  background runner lays every step out Pending and runs them in order, each
  moving InProgress → Success, Failed, TimedOut or Cancelled as the work
  settles, honouring `maxAttempts`, `timeoutSeconds`, `onFailure` (`Abort`,
  `Continue`, `step:`), `onCancel`, `nextStep`, `isEnd` and `isCritical`, and
  resolving `{{ }}` references to parameters, earlier steps' outputs and the
  `global:` and `automation:` variables. Steps call services through their
  APIs, as the runbook's `assumeRole` when it names one (Automation fails the
  execution with "The defined assume role is unable to be assumed." when it
  cannot) and otherwise with the starting caller's own credential:
  `aws:executeAwsApi`, `aws:assertAwsResourceProperty` and
  `aws:waitForAwsResourceProperty` reach any served operation by its Boto3
  namespace, `aws:runCommand` goes through `SendCommand`,
  `aws:invokeLambdaFunction` through `Invoke` and `aws:executeAutomation`
  through a child execution; `aws:sleep`, `aws:branch` and `aws:pause`
  (resumed by `SendAutomationSignal`) run in the runner. Any other action
  fails its step. Outputs come from each output's JSONPath selector over the
  response, converted to its declared type. `StopAutomationExecution` cancels
  the running step through the store, Interactive mode waits for `StartStep`,
  `Targets` with `ParameterValues` and `TargetMaps` run rate-controlled child
  automations, and `StartChangeRequestExecution` runs an
  `Automation.ChangeTemplate` and then its runbooks. Executions an earlier
  process left running resume from their step. `CreateDocument` and
  `UpdateDocument` read `schemaVersion` from JSON and YAML content, where
  every document had reported 2.2, and a missing document's `InvalidDocument`
  says "Document with name … does not exist.", the words terraform-provider-aws
  matches to see a deleted `aws_ssm_document` gone. The source is the vendored Automation
  actions reference and its per-action pages (`ssm-automation-*.md.gz`).
  `specs/cloud-api/aws/ssm.supplement.json` widens `AutomationActionName`'s
  pattern to the 26 letters `aws:waitForAwsResourceProperty` carries, which
  the model's `{3,25}` refused.
- **Amazon RDS global databases.** A DB cluster lives in the Region its
  request is signed for, as a DB instance does, so its ARN, endpoints and
  lookups name that Region. `CreateDBCluster` with `GlobalClusterIdentifier`
  joins the global database, as its primary when it has no member and
  otherwise as a read-only secondary in a Region no member uses, up to ten.
  `SwitchoverGlobalCluster` and `FailoverGlobalCluster` refuse a target that
  is no cluster or no member, or already the primary, and promote the
  secondary while the old primary becomes a secondary that the writer lists
  among its `Readers`; `RemoveFromGlobalCluster` detaches a secondary. The
  tests join a secondary from us-west-2 and switch over to it, where they had
  switched over to the cluster that already wrote.
- **AWS Step Functions events.** A standard workflow execution sends every
  status change to the default event bus as a "Step Functions Execution Status
  Change" event, as the Step Functions Developer Guide describes; express
  workflows send none. The CLI tests route their state machine's events to an
  Amazon SQS queue and long-poll it, where they had run the Python CLI's
  `describe-execution` every 100 ms.
- **AWS KMS signing key specs.** `ECC_NIST_EDWARDS25519` keys sign with
  `ED25519_SHA_512` over a RAW message and `ED25519_PH_SHA_512` over a SHA-512
  digest that KMS prehashes once more; `ML_DSA_44`, `ML_DSA_65` and
  `ML_DSA_87` keys sign with `ML_DSA_SHAKE_256` through circl, and
  `GetPublicKey` exports their SubjectPublicKeyInfo without a
  `CustomerMasterKeySpec`. `GenerateDataKeyPair` returns Ed25519 pairs and
  ML-KEM pairs whose PKCS#8 private key holds the seed. `Sign` and `Verify`
  take a DIGEST message as the digest, refusing one of the wrong length,
  where they had hashed it again.

- **Amazon VPC network ACLs.** Every VPC came with a default network ACL
  that allowed all traffic in and out ahead of the deny-all rules, IPv6 too
  when the VPC had an IPv6 block, as the vendored
  `vpc-default-network-acl.md.gz` lists, and every new subnet started
  associated with it. `ReplaceNetworkAclAssociation` refused an unknown
  association with `InvalidAssociationID.NotFound` where it had accepted a
  made-up one; `DeleteNetworkAcl` refused the default ACL and an ACL still
  associated with a subnet; deleting a subnet ended its association.
  `DescribeNetworkAcls` applied the association, entry and `owner-id`
  filters the API reference lists, which terraform-provider-aws needs to
  read one association by `association.association-id`; it had ignored
  them and answered every ACL once each VPC carried a default one.
- **Amazon EC2 security group rules and VPN tunnels.** `ReferencedGroupInfo`
  named the referenced group's owner as `UserId`; Terraform keeps the bare
  group ID because the owner is the caller's account, which the
  idempotency fixture's provider now requests from STS rather than skipping.
  `ModifyVpnTunnelCertificate` kept the tunnel's status where it had left the
  tunnel `DOWN`, and refused an outside address the connection does not have.
- **Amazon EC2 Capacity Reservations.** `RunInstances` claimed a reservation
  with matching instance type, platform, Availability Zone and tenancy that
  was active and had room, per `ec2-capacity-reservations-launch.md.gz`: an
  `open` preference (the default) took any open reservation and fell back to
  On-Demand capacity, a target took that reservation or failed with
  `ReservationCapacityExceeded`, and `capacity-reservations-only` failed
  without one. An instance held its slot until it terminated, so
  `AvailableInstanceCount` and `GetCapacityReservationUsage` counted live
  instances, and `DescribeInstances` returned the reservation and the
  launch's targeting. Tests that assert a reservation's counts make it
  targeted, because the suite's parallel launches claim open ones as AWS
  would.
- **Amazon CloudFront.** Origins carried `VpcOriginConfig` and distributions
  `ViewerMtlsConfig` and `ConnectionFunctionAssociation`, so
  `ListDistributionsByVpcOriginId`, `ListDistributionsByTrustStore` and
  `ListDistributionsByConnectionFunction` listed the distributions that
  reference the resource; the latter two resolved the identifier and refused
  an unknown one with `EntityNotFound`. Distribution tenants carried
  `Customizations`; `AssociateDistributionTenantWebACL` set a web ACL
  override that `DisassociateDistributionTenantWebACL` removed, and
  `ListDistributionTenantsByCustomization` matched the web ACL and
  certificate ARNs. `GetFunctionResult` binds no summary header, so
  `GetFunction` returns the code and ETag alone.
- **AWS CloudTrail Lake.** `StartQuery` parsed the statement and evaluated the
  SQL the vendored constraints page lists over one event data store, named by
  its ID in `FROM`: `SELECT [DISTINCT]` with aliases, `WHERE` with `AND`,
  `OR`, `NOT`, `IN`, `LIKE`, `BETWEEN`, `IS [NOT] NULL`,
  `IS [NOT] DISTINCT FROM` and the comparisons, `GROUP BY` with `COUNT`,
  `MIN`, `MAX`, `SUM` and `AVG`, `HAVING`, `ORDER BY` and `LIMIT`. Results
  keep the projection's column order, and a statement other than `SELECT`
  fails with `InvalidQueryStatementException`. Tests scope their rows with a
  dedicated access key, because the store records the whole parallel suite.
- **Amazon API Gateway and Amazon ElastiCache.** A new method setting
  override starts with the 300-second cache TTL the vendored caching guide
  gives. A Global Datastore ID takes its Region's prefix, `dsdfu` in
  US-West-1 as the API reference names.

## Continuous integration and dependencies

Jobs are held to fifteen minutes; a timeout kill reports as "cancelled", which
reads as infrastructure noise. A job starts loading its base images and
building its engine images before it compiles anything, so the loads overlap
the compile steps; the AWS SDK job once started them after, and its shard
that also runs the module unit tests ran out of budget in setup. Suites are sharded on measured cost with each
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
Every Go module fetch CI makes goes through bounded, retried attempts,
because the module proxy resets streams under load: `scripts/ci-go-mod-download.sh`
warms a module directory, or a `module@version` a document installs, and the
engine builds of the sidecar and the node agent run `go mod download` in the
same retried loop before `go build`.
The specification freshness check fetches each upstream page with curl's
bounded retries, because a documentation host dropping one request failed
the whole check.
A microVM job restores the node agent image built from the same sources, keyed
by them, and loads it; the agent's build finds it by its tag and skips the
apt, Firecracker and kernel downloads that once took a whole step's budget.
A simulator job loads its base images in the background
(`scripts/ci-base-images.sh`) while it compiles, and the engine-image builds
and the tests wait on that load's lock: in the foreground, restoring and
loading the set took thirteen of a shard's fifteen minutes on a slow runner and
cancelled the job before a test ran.

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
simulators outruns the build gates' budget. Every login to ghcr.io goes through
`.github/actions/ghcr-login`, which retries a login the registry does not
answer: docker/login-action tries once, and one timeout on the Azure arm64
runner skipped every manifest of v3.3.10, so its README image did not exist.
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
A job that only restores the base-image cache loads it with `--load-only`:
the race job had spent three of its fifteen minutes writing a tarball it
never saves, after a changed image set left it the nearest earlier archive.
Caddy, Terraform and Bun come from their release archives through the
`install-caddy`, `install-terraform` and `install-bun` composite actions, which keep the
verified archive in the Actions cache keyed on the pin. GitHub release
downloads had answered 504 through all eight of curl's retries and failed two
Terraform jobs before a test ran; with the cache, only the first job after a
pin moves downloads.

Dependencies of every class are held to their newest release past a 24-hour
quarantine; `ui/bunfig.toml` sets the same minimum release age, because caret
ranges let the resolver pick what the quarantine refuses. The check fails when
the proxy refuses a version listing: it once skipped such a module as current
and, on a full disk, reported everything current. Its self-test serves every
registry from fixtures, and reading `GOPROXY` runs with `GOTOOLCHAIN=local`.
An npm release is as young as the youngest release it pins exactly, followed
through those pins: the check had called `@playwright/test` 1.65.0 adoptable
while bun refused it for the hour-old `playwright-core` 1.65.0 it pins.
`scripts/check-repo-config-sane.sh` exists because a `git config` run without
`-C` in a linked worktree wrote a test fixture's identity into the shared
`.git/config`.
