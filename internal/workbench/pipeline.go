package workbench

import "context"

// Pipeline is the serve pipeline handed to the workbench as functions.
type Pipeline struct {
	// Evaluate runs the pipeline serve runs over a configuration's bytes and
	// reports everything it finds as data. It returns an error only when it
	// could not run at all (context cancelled).
	Evaluate func(ctx context.Context, config []byte) (*Snapshot, error)
	// Explain renders explain-call for one operation under a configuration,
	// without the network.
	Explain func(ctx context.Context, config []byte, key string, arguments []byte) (string, error)
}
