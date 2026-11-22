package files

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"kern/index"
)

// The commands' request ports, bound to the module's own verbs where the
// wiring binds them to nothing (index.RequestPort.Self), so the MCP door
// serves them: an agent reads and writes a managed file through them, as
// the refusal of a native write names (README.md "Agent tools").
var (
	readPort   = index.Request[Ref, File]("read-file", "Reads a managed file through the verb read.").Self("read")
	writePort  = index.Request[Change, File]("write-file", "Writes a managed file through the verb write.").Self("write")
	removePort = index.Request[Removal, Ref]("remove-file", "Removes a managed file through the verb remove.").Self("remove")
)

// doorParts are the commands the door serves as the tools files (reads) and
// files_write (writes), and the ports they ask the verbs on.
func (f *Files) doorParts() []index.Part {
	return []index.Part{
		readPort, writePort, removePort,
		index.Command(index.CommandDecl{
			Name: "files read", Usage: "files read --path PATH",
			Summary: "Read a managed file and its version.", Group: "Files", Class: index.Interactive, ReadOnly: true,
			Help: "Reads the managed file PATH, relative to the checkout's root, from the model: its first line names its version, the rest is its content. A write names that version.",
			MCP:  "Reads a file in a managed folder, with the version a write of it names: use it before files edit, files write or files remove, in place of reading the file natively when you will change it.",
		}, func(_ struct{}, args []string) (index.Result, error) {
			fs, p := pathFlags("files read")
			if err := parse(fs, args, p); err != nil {
				return index.Result{}, err
			}
			return index.Result{Requests: []index.Ask{readPort.Ask(Ref{Path: *p})}, Then: func(ans []any) (index.Result, error) {
				file := readPort.Answer(ans[0])
				return index.Result{Text: fmt.Sprintf("%s at version %s (%d bytes):\n%s", file.Path, file.Hash, file.Size, file.Content)}, nil
			}}, nil
		}),
		index.Command(index.CommandDecl{
			Name: "files edit", Usage: "files edit --path PATH --version V --old TEXT --new TEXT [--all]",
			Summary: "Replace a string in a managed file.", Group: "Files", Class: index.Batch, Writes: true,
			Help: "Replaces TEXT in the managed file PATH, which must stand at version V, the one files read named: --old must occur once, or --all replaces every occurrence. A file changed since is refused, naming re-read.",
			MCP:  "Edits a file in a managed folder, in place of a native Edit, which kern refuses there: replaces --old with --new, naming the --version files read answered; a file changed since is refused, naming re-read.",
		}, func(_ struct{}, args []string) (index.Result, error) {
			fs, p := pathFlags("files edit")
			version := fs.String("version", "", "the version files read named")
			old := fs.String("old", "", "the text to replace")
			repl := fs.String("new", "", "the text it becomes")
			all := fs.Bool("all", false, "replace every occurrence")
			if err := parse(fs, args, p); err != nil {
				return index.Result{}, err
			}
			if *old == "" {
				return index.Result{}, fmt.Errorf("files edit names --old, the text to replace")
			}
			return index.Result{Requests: []index.Ask{readPort.Ask(Ref{Path: *p})}, Then: func(ans []any) (index.Result, error) {
				file := readPort.Answer(ans[0])
				if file.Hash != *version {
					return index.Result{}, stale("edit", file.Path, *version, file.Hash)
				}
				n := strings.Count(file.Content, *old)
				switch {
				case n == 0:
					return index.Result{}, fmt.Errorf("files edit %s: the old text is not in %s at version %s: re-read it", file.Path, file.Path, file.Hash)
				case n > 1 && !*all:
					return index.Result{}, fmt.Errorf("files edit %s: the old text occurs %d times: give more of its context, or --all to replace each", file.Path, n)
				}
				content := strings.Replace(file.Content, *old, *repl, -1)
				return index.Result{Requests: []index.Ask{writePort.Ask(Change{Path: file.Path, Content: content, Version: file.Hash})}, Then: func(ans []any) (index.Result, error) {
					w := writePort.Answer(ans[0])
					return index.Result{Text: fmt.Sprintf("edited %s, now at version %s", w.Path, w.Hash)}, nil
				}}, nil
			}}, nil
		}),
		index.Command(index.CommandDecl{
			Name: "files write", Usage: "files write --path PATH --version V --content TEXT",
			Summary: "Write a managed file whole.", Group: "Files", Class: index.Batch, Writes: true,
			Help: "Writes TEXT as the whole of the managed file PATH, which must stand at version V, the one files read named, or not stand where V is empty. A file changed since is refused, naming re-read.",
			MCP:  "Writes a file in a managed folder whole, in place of a native Write, which kern refuses there: names the --version files read answered, or --version \"\" for a new file; a file changed since is refused, naming re-read.",
		}, func(_ struct{}, args []string) (index.Result, error) {
			fs, p := pathFlags("files write")
			version := fs.String("version", "", "the version files read named, empty for a new file")
			content := fs.String("content", "", "the file's whole content")
			if err := parse(fs, args, p); err != nil {
				return index.Result{}, err
			}
			return index.Result{Requests: []index.Ask{writePort.Ask(Change{Path: *p, Content: *content, Version: *version})}, Then: func(ans []any) (index.Result, error) {
				w := writePort.Answer(ans[0])
				return index.Result{Text: fmt.Sprintf("wrote %s, now at version %s", w.Path, w.Hash)}, nil
			}}, nil
		}),
		index.Command(index.CommandDecl{
			Name: "files remove", Usage: "files remove --path PATH --version V",
			Summary: "Remove a managed file.", Group: "Files", Class: index.Batch, Writes: true,
			Help: "Removes the managed file PATH, which must stand at version V, the one files read named. A file changed since is refused, naming re-read.",
			MCP:  "Removes a file in a managed folder, in place of a native rm, which kern refuses there: names the --version files read answered; a file changed since is refused, naming re-read.",
		}, func(_ struct{}, args []string) (index.Result, error) {
			fs, p := pathFlags("files remove")
			version := fs.String("version", "", "the version files read named")
			if err := parse(fs, args, p); err != nil {
				return index.Result{}, err
			}
			return index.Result{Requests: []index.Ask{removePort.Ask(Removal{Path: *p, Version: *version})}, Then: func(ans []any) (index.Result, error) {
				return index.Result{Text: "removed " + removePort.Answer(ans[0]).Path}, nil
			}}, nil
		}),
	}
}

func pathFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs, fs.String("path", "", "the managed file, relative to the checkout's root")
}

func parse(fs *flag.FlagSet, args []string, p *string) error {
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%s: %v", fs.Name(), err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%s takes no argument %q: name the file with --path", fs.Name(), fs.Arg(0))
	}
	if *p == "" {
		return fmt.Errorf("%s names --path, the managed file relative to the checkout's root", fs.Name())
	}
	return nil
}
