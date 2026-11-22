// Package fixture holds the components the index and the fake core are
// tested with. They are no real components: nothing but tests imports them.
package fixture

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"kern/index"
)

type totalIn struct {
	Numbers []int `index:"numbers"`
	Scale   int   `index:"config/scale"`
}

type noteIn struct {
	Numbers []int `index:"numbers"`
}

// Note is the last note the note command took.
var Note = index.Output("note", "", "The last note taken.")

// Tally sums the numbers it reads, scaled, and takes notes.
var Tally = index.Declare("tally", "Sums numbers and takes notes.",
	index.Config("scale", 1, "What the sum is multiplied by."),
	index.Derived("total", 0, "The sum of the numbers, scaled.", func(in totalIn) (int, error) {
		sum := 0
		for _, n := range in.Numbers {
			sum += n
		}
		return sum * in.Scale, nil
	}),
	Note,
	index.Command(index.CommandDecl{
		Name:    "tally note",
		Usage:   "tally note WORDS...",
		Summary: "Take a note.",
		Help:    "Keeps the words as the last note, and says how many numbers stand.",
		Group:   "Fixtures",
		Session: true,
		Class:   index.Batch,
	}, func(in noteIn, args []string) (index.Result, error) {
		if len(args) == 0 {
			return index.Result{}, fmt.Errorf("tally note takes the words to keep")
		}
		note := strings.Join(args, " ")
		return index.Result{
			Text:   fmt.Sprintf("noted %q beside %d numbers", note, len(in.Numbers)),
			Writes: []index.Write{Note.Set(note)},
		}, nil
	}),
)

func init() { index.Register(Tally) }

// Ledger is an I/O component whose outside thing is the list of lines it is
// handed: its verb append adds a line and answers how many lines stand, and
// refuses an empty one.
func Ledger(lines *[]string) *index.Component {
	return index.Declare("ledger", "Keeps the lines it is asked to append.",
		index.IO(),
		index.Verb("append", "Appends a line and answers how many lines stand.", func(_ context.Context, line string) (int, error) {
			if line == "" {
				return 0, fmt.Errorf("the ledger takes no empty line")
			}
			*lines = append(*lines, line)
			return len(*lines), nil
		}),
	)
}

// Post is the clerk's request port to a ledger's append.
var Post = index.Request[string, int]("post", "Appends a line to a ledger.")

// Filed is the clerk's last entry and where it stands.
var Filed = index.Output("filed", "", "The last entry filed, with its number.")

// Clerk is a compute component that acts on the outside through its request
// port: clerk file posts each word as its own line, then commits where the last one stands.
var Clerk = index.Declare("clerk", "Files words in a ledger.",
	Post, Filed,
	index.Command(index.CommandDecl{
		Name:    "clerk file",
		Usage:   "clerk file WORDS...",
		Summary: "File each word as a line.",
		Help:    "Posts each word to the ledger in order; a refused post stops the rest.",
		Group:   "Fixtures",
		Class:   index.Batch,
	}, func(_ struct{}, args []string) (index.Result, error) {
		asks := make([]index.Ask, len(args))
		for i, a := range args {
			asks[i] = Post.Ask(a)
		}
		return index.Result{
			Text:     fmt.Sprintf("filing %d words", len(args)),
			Requests: asks,
			Then: func(answers []any) (index.Result, error) {
				if len(answers) == 0 {
					return index.Result{Text: "nothing filed"}, nil
				}
				entry := fmt.Sprintf("%s as line %d", args[len(args)-1], Post.Answer(answers[len(answers)-1]))
				return index.Result{Text: "filed " + entry, Writes: []index.Write{Filed.Set(entry)}}, nil
			},
		}, nil
	}),
)

type countIn struct {
	Upto int `index:"config/upto"`
}

// Counter writes the numbers 1 to its config upto: the writer that claims
// and meets what tally expects of the numbers it reads (counter.expect.toml).
var Counter = index.Declare("counter", "Counts from 1 up to upto.",
	index.Config("upto", 0, "The last number counted."),
	index.Derived("numbers", []int{}, "The numbers 1 to upto.", func(in countIn) ([]int, error) {
		numbers := []int{}
		for n := 1; n <= in.Upto; n++ {
			numbers = append(numbers, n)
		}
		return numbers, nil
	}),
)

// IssueRef is what the watcher asks of an issue.
type IssueRef struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

// IssueData is what the watcher reads of an issue as its tracker answers it.
type IssueData struct {
	Title string `json:"title"`
	State string `json:"state"`
}

// IssueRead is what the watcher relies on of an issue read: which issue,
// when it was read, and its title and state.
type IssueRead struct {
	Repo   string    `json:"repo"`
	Number int       `json:"number"`
	Read   time.Time `json:"read"`
	Data   IssueData `json:"data"`
}

// Issue is the watcher's request port to an issue read.
var Issue = index.Request[IssueRef, IssueRead]("issue", "Reads an issue.")

type watchIn struct {
	Issues map[string]IssueRead `index:"issues"`
}

// Watcher reads issues of an outside tracker through names alone: the reader
// whose expectations of the issue reads an I/O module's hot test answers
// with real recordings (watcher.expect.toml, README.md "Hot tests").
var Watcher = index.Declare("watcher", "Watches issues.",
	Issue,
	index.Derived("titles", []string{}, "Each issue read, as <number> <state>: <title>, in key order.", func(in watchIn) ([]string, error) {
		keys := make([]string, 0, len(in.Issues))
		for k := range in.Issues {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		titles := make([]string, 0, len(keys))
		for _, k := range keys {
			i := in.Issues[k]
			titles = append(titles, fmt.Sprintf("%d %s: %s", i.Number, i.Data.State, i.Data.Title))
		}
		return titles, nil
	}),
	index.Command(index.CommandDecl{
		Name:    "watch",
		Usage:   "watch OWNER REPO NUMBER",
		Summary: "Read one issue.",
		Help:    "Reads the issue and says its title and its state.",
		Group:   "Fixtures",
		Class:   index.Batch,
	}, func(_ struct{}, args []string) (index.Result, error) {
		if len(args) != 3 {
			return index.Result{}, fmt.Errorf("watch takes an owner, a repository and a number")
		}
		n, err := strconv.Atoi(args[2])
		if err != nil {
			return index.Result{}, fmt.Errorf("watch: %q is no number", args[2])
		}
		return index.Result{
			Requests: []index.Ask{Issue.Ask(IssueRef{Owner: args[0], Repo: args[1], Number: n})},
			Then: func(answers []any) (index.Result, error) {
				i := Issue.Answer(answers[0])
				return index.Result{Text: fmt.Sprintf("%s issue %d %s: %s", i.Repo, i.Number, i.Data.State, i.Data.Title)}, nil
			},
		}, nil
	}),
)
