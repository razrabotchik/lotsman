package searchindex_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/openapi"
	"github.com/razrabotchik/lotsman/internal/searchindex"
)

// task is one thing a person might ask an agent to do, and the operation that
// would actually do it. The queries are written the way someone types them,
// not the way the document spells them -- a benchmark of the document's own
// vocabulary measures nothing.
type task struct {
	query string
	want  domain.OperationKey
}

// The benchmark is committed *before* any ranking is tuned (FR-54). Its job is
// to make a tuning claim checkable: a change that improves one query and breaks
// three shows up here as a number going down.
var kubernetesTasks = []task{
	{"list deployments", "default:GET:/apis/apps/v1/deployments"},
	{"list deployments in a namespace", "default:GET:/apis/apps/v1/namespaces/{namespace}/deployments"},
	{"create a deployment", "default:POST:/apis/apps/v1/namespaces/{namespace}/deployments"},
	{"scale a deployment", "default:PUT:/apis/apps/v1/namespaces/{namespace}/deployments/{name}/scale"},
	{"read the scale of a statefulset", "default:GET:/apis/apps/v1/namespaces/{namespace}/statefulsets/{name}/scale"},
	{"delete a daemonset", "default:DELETE:/apis/apps/v1/namespaces/{namespace}/daemonsets/{name}"},
	{"status of a statefulset", "default:GET:/apis/apps/v1/namespaces/{namespace}/statefulsets/{name}/status"},
	{"watch replicasets", "default:GET:/apis/apps/v1/watch/namespaces/{namespace}/replicasets"},
	{"list controller revisions", "default:GET:/apis/apps/v1/controllerrevisions"},
	{"replace a statefulset", "default:PUT:/apis/apps/v1/namespaces/{namespace}/statefulsets/{name}"},
}

var digitalOceanTasks = []task{
	{"list droplets", "default:GET:/v2/droplets"},
	{"create a droplet", "default:POST:/v2/droplets"},
	{"delete a droplet", "default:DELETE:/v2/droplets/{droplet_id}"},
	{"list kubernetes clusters", "default:GET:/v2/kubernetes/clusters"},
	{"account information", "default:GET:/v2/account"},
	{"list database clusters", "default:GET:/v2/databases"},
	{"create a load balancer", "default:POST:/v2/load_balancers"},
	{"list ssh keys", "default:GET:/v2/account/keys"},
	{"get my billing balance", "default:GET:/v2/customers/my/balance"},
	{"list firewalls", "default:GET:/v2/firewalls"},
	{"power off a droplet", "default:POST:/v2/droplets/{droplet_id}/actions"},
	{"list container registry repositories", "default:GET:/v2/registry/{registry_name}/repositoriesV2"},
}

// Thresholds are the measured baseline minus a margin, not aspirations: they
// exist to catch a regression, and a threshold nobody can meet is a test that
// gets commented out.
//
// The two corpora fail in opposite directions, and the numbers say so:
//
//   - Kubernetes: Recall@5 1.00, MRR 0.53. Everything is findable and little is
//     first, because its operations are near-duplicates -- cluster-wide vs
//     namespaced vs watch differ by one path segment, and a person's query
//     almost never says which they meant. Ranking cannot fix an ambiguity that
//     is in the question.
//   - DigitalOcean: Recall@5 0.75, MRR 0.65. When it finds the operation it
//     usually ranks it first; the misses are morphology ("droplet" does not
//     match "droplets") and intent that no word carries ("power off" is a POST
//     to an actions endpoint, and nothing lexical says so).
func TestSearchRecallOnCorpus(t *testing.T) {
	for _, corpus := range []struct {
		name         string
		spec         string
		tasks        []task
		minRecallAt5 float64
		minMRR       float64
	}{
		{"kubernetes", "kubernetes-apps-v1.json", kubernetesTasks, 0.90, 0.45},
		{"digitalocean", "digitalocean-exploded/specification/DigitalOcean-public.v2.yaml", digitalOceanTasks, 0.70, 0.55},
	} {
		t.Run(corpus.name, func(t *testing.T) {
			index := indexCorpus(t, corpus.spec)

			recall, mrr, misses := measure(index, corpus.tasks)
			t.Logf("Recall@5 = %.2f, MRR = %.2f over %d tasks", recall, mrr, len(corpus.tasks))
			for _, miss := range misses {
				t.Logf("  miss: %s", miss)
			}

			if recall < corpus.minRecallAt5 {
				t.Errorf("Recall@5 = %.2f, want at least %.2f", recall, corpus.minRecallAt5)
			}
			if mrr < corpus.minMRR {
				t.Errorf("MRR = %.2f, want at least %.2f", mrr, corpus.minMRR)
			}
		})
	}
}

// measure computes Recall@5 (did the right operation appear in the top five?)
// and MRR (how far down was it?). MRR is the one that notices a change from
// "first" to "fourth", which recall alone cannot see.
func measure(index *searchindex.Index, tasks []task) (recall, mrr float64, misses []string) {
	for _, item := range tasks {
		results := index.Search(item.query, searchindex.Filters{}, 5)
		rank := 0
		for i := range results {
			if results[i].Key == item.want {
				rank = i + 1
				break
			}
		}
		switch rank {
		case 0:
			top := "nothing"
			if len(results) > 0 {
				top = string(results[0].Key)
			}
			misses = append(misses, fmt.Sprintf("%q wanted %s, top hit %s", item.query, item.want, top))
		default:
			recall++
			mrr += 1 / float64(rank)
		}
	}
	count := float64(len(tasks))
	return recall / count, mrr / count, misses
}

func indexCorpus(t *testing.T, name string) *searchindex.Index {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "corpus")
	path := filepath.Join(root, name)
	spec, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("corpus document not fetched; run `make corpus`")
	}
	doc, err := openapi.Parse(t.Context(), spec, openapi.Options{RootPath: filepath.Dir(path)})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cat := catalog.Build("sha256:bench", doc.Operations, catalog.Options{})
	return searchindex.FromCatalog(&cat)
}
