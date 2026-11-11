package reporting

import "slices"

// The data class of a result (S08-A, docs/ADR_0012_MANUAL_DATASETS.md decision 6): the word that says where its data comes from, so that
// synthetic and typed-in data are marked wherever they show and a source product's data is not called synthetic. It is the adapter's
// word, one of a closed set of three:
//
//   - contract-simulator: the synthetic data of a contract simulator, never real business data;
//   - manual-entry: data that people typed in or pasted (the manual source of ADR 0012), not a source product's;
//   - business-data: the data of a source product, which is what every adapter that says nothing serves.
//
// A result's evidence carries it (Evidence.DataClass), a frozen snapshot keeps the word it was frozen with, and the exports print it. The
// word says where the data comes from and nothing of whether it is true: the platform cannot know that.
const (
	DataClassSimulator = "contract-simulator"
	DataClassManual    = "manual-entry"
	DataClassBusiness  = "business-data"
)

// DataClasses is the closed set of data classes, in the order of the ADR.
var DataClasses = []string{DataClassSimulator, DataClassManual, DataClassBusiness}

// ValidDataClass says whether a word is one of the three data classes.
func ValidDataClass(class string) bool { return slices.Contains(DataClasses, class) }

// ClassedAdapter is the optional capability of an adapter that says the class of the data it serves, in the manner of DocumentAuthority
// and CompanyDirectory: an adapter has it or not by its type. The contract simulators say contract-simulator and the manual source says
// manual-entry. An adapter of a source product needs none: an adapter without the capability serves business-data.
//
// The word is the adapter's as a whole, not a dataset's, a principal's or a read's, and the service reads it once, when it is made:
// NewService refuses a connection whose adapter says a word outside the set. A wrapper that embeds only the Adapter interface hides the
// capability of what it wraps, as it hides every other optional one, and then serves business-data: a wrapper of a simulator must say
// the simulator's word itself.
type ClassedAdapter interface {
	DataClass() string
}

// DataClassOf is the data class an adapter serves: its own word when it is a ClassedAdapter (whatever the word is: NewService judges it),
// business-data when it says none.
func DataClassOf(a Adapter) string {
	if c, ok := a.(ClassedAdapter); ok {
		return c.DataClass()
	}
	return DataClassBusiness
}
