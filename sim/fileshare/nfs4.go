package fileshare

// NFSv4.0 (RFC 7530) operation numbers, status codes and attribute numbers.

const (
	opAccess           = 3
	opClose            = 4
	opCommit           = 5
	opCreate           = 6
	opDelegPurge       = 7
	opDelegReturn      = 8
	opGetAttr          = 9
	opGetFH            = 10
	opLink             = 11
	opLock             = 12
	opLockT            = 13
	opLockU            = 14
	opLookup           = 15
	opLookupP          = 16
	opNVerify          = 17
	opOpen             = 18
	opOpenAttr         = 19
	opOpenConfirm      = 20
	opOpenDowngrade    = 21
	opPutFH            = 22
	opPutPubFH         = 23
	opPutRootFH        = 24
	opRead             = 25
	opReadDir          = 26
	opReadLink         = 27
	opRemove           = 28
	opRename           = 29
	opRenew            = 30
	opRestoreFH        = 31
	opSaveFH           = 32
	opSecInfo          = 33
	opSetAttr          = 34
	opSetClientID      = 35
	opSetClientIDConf  = 36
	opVerify           = 37
	opWrite            = 38
	opReleaseLockOwner = 39
	opIllegal          = 10044
)

const (
	nfs4OK                   = 0
	nfs4ErrPerm              = 1
	nfs4ErrNoEnt             = 2
	nfs4ErrIO                = 5
	nfs4ErrNXIO              = 6
	nfs4ErrAccess            = 13
	nfs4ErrExist             = 17
	nfs4ErrXDev              = 18
	nfs4ErrNotDir            = 20
	nfs4ErrIsDir             = 21
	nfs4ErrInval             = 22
	nfs4ErrFBig              = 27
	nfs4ErrNoSpc             = 28
	nfs4ErrROFS              = 30
	nfs4ErrMLink             = 31
	nfs4ErrNameTooLong       = 63
	nfs4ErrNotEmpty          = 66
	nfs4ErrDQuot             = 69
	nfs4ErrStale             = 70
	nfs4ErrBadHandle         = 10001
	nfs4ErrBadCookie         = 10003
	nfs4ErrNotSupp           = 10004
	nfs4ErrTooSmall          = 10005
	nfs4ErrServerFault       = 10006
	nfs4ErrBadType           = 10007
	nfs4ErrDenied            = 10010
	nfs4ErrExpired           = 10011
	nfs4ErrLocked            = 10012
	nfs4ErrGrace             = 10013
	nfs4ErrFHExpired         = 10014
	nfs4ErrShareDenied       = 10015
	nfs4ErrWrongSec          = 10016
	nfs4ErrClidInUse         = 10017
	nfs4ErrResource          = 10018
	nfs4ErrMoved             = 10019
	nfs4ErrNoFileHandle      = 10020
	nfs4ErrMinorVersMismatch = 10021
	nfs4ErrStaleClientID     = 10022
	nfs4ErrStaleStateID      = 10023
	nfs4ErrOldStateID        = 10024
	nfs4ErrBadStateID        = 10025
	nfs4ErrBadSeqID          = 10026
	nfs4ErrNotSame           = 10027
	nfs4ErrLockRange         = 10028
	nfs4ErrSymlink           = 10029
	nfs4ErrRestoreFH         = 10030
	nfs4ErrLeaseMoved        = 10031
	nfs4ErrAttrNotSupp       = 10032
	nfs4ErrNoGrace           = 10033
	nfs4ErrReclaimBad        = 10034
	nfs4ErrReclaimConflict   = 10035
	nfs4ErrBadXDR            = 10036
	nfs4ErrLocksHeld         = 10037
	nfs4ErrOpenMode          = 10038
	nfs4ErrBadOwner          = 10039
	nfs4ErrBadChar           = 10040
	nfs4ErrBadName           = 10041
	nfs4ErrBadRange          = 10042
	nfs4ErrLockNotSupp       = 10043
	nfs4ErrOpIllegal         = 10044
	nfs4ErrDeadlock          = 10045
	nfs4ErrFileOpen          = 10046
	nfs4ErrAdminRevoked      = 10047
	nfs4ErrCBPathDown        = 10048
)

// Attribute numbers (RFC 7530 section 5).
const (
	attrSupportedAttrs  = 0
	attrType            = 1
	attrFHExpireType    = 2
	attrChange          = 3
	attrSize            = 4
	attrLinkSupport     = 5
	attrSymlinkSupport  = 6
	attrNamedAttr       = 7
	attrFSID            = 8
	attrUniqueHandles   = 9
	attrLeaseTime       = 10
	attrRdattrError     = 11
	attrACL             = 12
	attrACLSupport      = 13
	attrArchive         = 14
	attrCanSetTime      = 15
	attrCaseInsensitive = 16
	attrCasePreserving  = 17
	attrChownRestricted = 18
	attrFileHandle      = 19
	attrFileID          = 20
	attrFilesAvail      = 21
	attrFilesFree       = 22
	attrFilesTotal      = 23
	attrFSLocations     = 24
	attrHidden          = 25
	attrHomogeneous     = 26
	attrMaxFileSize     = 27
	attrMaxLink         = 28
	attrMaxName         = 29
	attrMaxRead         = 30
	attrMaxWrite        = 31
	attrMimeType        = 32
	attrMode            = 33
	attrNoTrunc         = 34
	attrNumLinks        = 35
	attrOwner           = 36
	attrOwnerGroup      = 37
	attrQuotaAvailHard  = 38
	attrQuotaAvailSoft  = 39
	attrQuotaUsed       = 40
	attrRawDev          = 41
	attrSpaceAvail      = 42
	attrSpaceFree       = 43
	attrSpaceTotal      = 44
	attrSpaceUsed       = 45
	attrSystem          = 46
	attrTimeAccess      = 47
	attrTimeAccessSet   = 48
	attrTimeBackup      = 49
	attrTimeCreate      = 50
	attrTimeDelta       = 51
	attrTimeMetadata    = 52
	attrTimeModify      = 53
	attrTimeModifySet   = 54
	attrMountedOnFileID = 55
)

// File types (nfs_ftype4).
const (
	nf4Reg       = 1
	nf4Dir       = 2
	nf4Blk       = 3
	nf4Chr       = 4
	nf4Lnk       = 5
	nf4Sock      = 6
	nf4Fifo      = 7
	nf4AttrDir   = 8
	nf4NamedAttr = 9
)

const (
	fh4Persistent = 0

	accessRead    = 0x01
	accessLookup  = 0x02
	accessModify  = 0x04
	accessExtend  = 0x08
	accessDelete  = 0x10
	accessExecute = 0x20

	openNoCreate = 0
	openCreate   = 1

	createUnchecked = 0
	createGuarded   = 1
	createExclusive = 2

	claimNull         = 0
	claimPrevious     = 1
	claimDelegateCur  = 2
	claimDelegatePrev = 3

	shareAccessRead  = 1
	shareAccessWrite = 2
	shareAccessBoth  = 3

	openDelegateNone = 0

	open4ResultConfirm       = 2
	open4ResultLockTypePosix = 4

	unstable4 = 0
	dataSync4 = 1
	fileSync4 = 2

	setToServerTime4 = 0
	setToClientTime4 = 1

	readLT   = 1
	writeLT  = 2
	readWLT  = 3
	writeWLT = 4

	// maxIO is the largest READ and WRITE the server advertises.
	maxIO = 1 << 20

	// leaseSeconds is the lease a client renews with RENEW or any other op.
	leaseSeconds = 90
)
