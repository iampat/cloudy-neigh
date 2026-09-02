package kvfs

// Export internal primitives for white-box testing in kvfs_test.
var (
	PutBlob       = putBlob
	GetBlob       = getBlob
	ExistsBlob    = existsBlob
	PutManifest   = putManifest
	GetManifest   = getManifest
	ResolveBranch = resolveBranch
	UpdateBranch  = updateBranch
	CreateBranch  = createBranch
)
