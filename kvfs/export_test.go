package kvfs

var (
	PutBlob        = putBlob
	GetBlob        = getBlob
	PutManifest    = putManifest
	GetManifest    = getManifest
	ResolveBranch  = resolveBranch
	UpdateBranch   = updateBranch
	CreateBranch   = createBranch
	ShardedKey     = shardedKey
	ValidateHash   = validateHash
	CasPrefix      = casPrefix
	ManifestPrefix = manifestPrefix
)
