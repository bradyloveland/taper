package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bradyloveland/taper/internal/store"
)

// postFiles posts a multipart form with the CSRF token and files (name to
// content) in the "files" field.
func (b *browser) postFiles(path string, form url.Values, files map[string]string) *resp {
	b.e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("csrf", b.csrf())
	for k, vs := range form {
		for _, v := range vs {
			mw.WriteField(k, v)
		}
	}
	for name, content := range files {
		fw, _ := mw.CreateFormFile("files", name)
		fw.Write([]byte(content))
	}
	mw.Close()
	req, _ := http.NewRequest("POST", b.url()+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return b.do(req)
}

var assignIDRE = regexp.MustCompile(`^/assignments/(\d+)$`)
var fileLinkRE = regexp.MustCompile(`href="(/files/\d+/[^"]+)"`)

func assignmentID(t *testing.T, r *resp) string {
	t.Helper()
	m := assignIDRE.FindStringSubmatch(r.Location)
	if r.Status != http.StatusSeeOther || m == nil {
		t.Fatalf("expected a redirect to the assignment, got %d %q\n%s", r.Status, r.Location, r.Body)
	}
	return m[1]
}

func TestAssignmentWorkflow(t *testing.T) {
	c := newCalEnv(t)
	classPath := fmt.Sprintf("/classes/%d", c.class.ID)
	c.addUser("otto", "Otto Other", store.RoleMentor, "mentor-password", false)

	m := c.signedIn("mia", "mentor-password")
	expect(t, m.get(classPath), http.StatusOK, "No assignments yet", classPath+"/assignments/new")
	expect(t, m.get(classPath+"/assignments/new"), http.StatusOK, "New assignment", `enctype="multipart/form-data"`)
	expect(t, m.postFiles(classPath+"/assignments/new", url.Values{"title": {" "}}, nil), http.StatusUnprocessableEntity, "Enter a title")
	expect(t, m.postFiles(classPath+"/assignments/new", url.Values{"title": {"Essay"}, "due_time": {"15:00"}}, nil),
		http.StatusUnprocessableEntity, "Choose a due date")

	r := m.postFiles(classPath+"/assignments/new", url.Values{"title": {"Read and respond"}, "instructions": {"Read **chapter 3**."},
		"due_date": {c.tomorrow}, "due_time": {"15:00"}, "publish": {"now"}, "work_online": {"1"}, "allow_files": {"1"}},
		map[string]string{"chapter-3.txt": "It was a dark and stormy night."})
	id := assignmentID(t, r)
	path := "/assignments/" + id
	page := m.get(path)
	expect(t, page, http.StatusOK, "Read and respond is published", "<strong>chapter 3</strong>", "chapter-3.txt", "Due tomorrow at 3:00 PM", "Sam Scholar", "Not turned in")
	attachment := fileLinkRE.FindStringSubmatch(page.Body)[1]

	// A draft stays hidden from scholars, and off the calendar.
	draft := assignmentID(t, m.postFiles(classPath+"/assignments/new", url.Values{"title": {"Secret plan"}, "due_date": {c.tomorrow}, "publish": {"draft"}}, nil))
	expect(t, m.get("/assignments/"+draft), http.StatusOK, "Draft: scholars can't see it yet")

	// The scholar sees it, downloads the file, and it's due soon.
	s := c.signedIn("sam", "scholar-password")
	expect(t, s.get("/"), http.StatusOK, `id="assignments"`, "Read and respond", "Not turned in")
	expect(t, s.get("/assignments/"+draft), http.StatusNotFound)
	if strings.Contains(s.get("/").Body, "Secret plan") || strings.Contains(s.get(classPath).Body, "Secret plan") {
		t.Fatal("scholars must not see drafts")
	}
	dl := s.get(attachment)
	expect(t, dl, http.StatusOK, "dark and stormy")
	if !strings.Contains(dl.Header.Get("Content-Security-Policy"), "sandbox") || dl.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("file headers: %v", dl.Header)
	}
	expect(t, s.get(path), http.StatusOK, "Your work", `data-autosave="`+path+`/work/autosave"`, "Turn in")
	expect(t, s.get("/calendar?view=list&date="+c.today), http.StatusOK, "Due: Read and respond", `href="`+path+`"`)
	if strings.Contains(s.last, "Secret plan") {
		t.Fatal("drafts must not be on the calendar")
	}

	// Someone outside the class sees nothing.
	z := c.signedIn("zoe", "scholar-password")
	expect(t, z.get(path), http.StatusNotFound)
	expect(t, z.get(attachment), http.StatusNotFound)

	// Turning in nothing isn't allowed; autosave keeps the writing.
	expectRedirect(t, s.postFiles(path+"/work", url.Values{"action": {"turn_in"}}, nil), path+"#work")
	expect(t, s.get(path), http.StatusOK, "nothing to turn in yet")
	s.get(path)
	r = s.postForm(path+"/work/autosave", url.Values{"body": {"My first thoughts."}})
	expect(t, r, http.StatusOK, `"saved"`)
	expect(t, s.get(path), http.StatusOK, "My first thoughts.")
	expectRedirect(t, s.postFiles(path+"/work", url.Values{"body": {"My answer:\nline two"}, "action": {"save"}},
		map[string]string{"answer.txt": "attached answer"}), path+"#work")
	expect(t, s.get(path), http.StatusOK, "Saved, with 1 new file", "answer.txt", "line two")
	work := fileLinkRE.FindAllStringSubmatch(s.last, -1)
	workFile := work[len(work)-1][1]
	expectRedirect(t, s.postFiles(path+"/work", url.Values{"body": {"My answer:\nline two"}, "action": {"turn_in"}}, nil), path+"#work")
	expect(t, s.get(path), http.StatusOK, "Turned in. Your mentor will look at it.", "Take it back")
	expect(t, s.postForm(path+"/work/autosave", url.Values{"body": {"changed"}}), http.StatusConflict)

	// Taking it back and turning it in again.
	expectRedirect(t, s.postForm(path+"/work/take-back", nil), path+"#work")
	expect(t, s.get(path), http.StatusOK, "Taken back", "Turn in")
	expectRedirect(t, s.postFiles(path+"/work", url.Values{"body": {"My answer:\nline two"}, "action": {"turn_in"}}, nil), path+"#work")

	// Others can't see the work file; the mentor can.
	expect(t, z.get(workFile), http.StatusNotFound)
	o := c.signedIn("otto", "mentor-password")
	expect(t, o.get(path), http.StatusOK, "Read and respond")
	if strings.Contains(o.last, "Scholars' work") {
		t.Fatal("a mentor outside the class must not see the roster")
	}
	expect(t, o.get(workFile), http.StatusNotFound)
	expect(t, o.get(fmt.Sprintf("%s/work/%d", path, c.sam.ID)), http.StatusNotFound)
	expect(t, m.get(workFile), http.StatusOK, "attached answer")

	// The mentor reviews it.
	expect(t, m.get("/"), http.StatusOK, `id="assignments"`, "1 to review", "Read and respond")
	if strings.Contains(m.last, "Secret plan") {
		t.Fatal("drafts aren't due soon")
	}
	expect(t, m.get(path), http.StatusOK, "1 to review", "Review")
	review := fmt.Sprintf("%s/work/%d", path, c.sam.ID)
	expect(t, m.get(review), http.StatusOK, "Sam Scholar", "My answer:<br>", "answer.txt", "Mark complete")
	expectRedirect(t, m.postForm(review, url.Values{"decision": {"needs_work"}}), review)
	expect(t, m.get(review), http.StatusOK, "Say what needs another look")
	expectRedirect(t, m.postForm(review, url.Values{"decision": {"needs_work"}, "feedback": {"Add an example."}}), path+"#roster")
	expect(t, s.get(path), http.StatusOK, "Feedback from your mentor", "Add an example.", "Needs another look", "Turn in again")
	expectRedirect(t, s.postFiles(path+"/work", url.Values{"body": {"My answer, with an example."}, "action": {"turn_in"}}, nil), path+"#work")
	expectRedirect(t, m.postForm(review, url.Values{"decision": {"complete"}, "feedback": {"Well done."}}), path+"#roster")
	expect(t, s.get(path), http.StatusOK, "Complete", "Well done.", "Marked complete by Mia Mentor")
	expect(t, s.postFiles(path+"/work", url.Values{"body": {"sneaky"}, "action": {"save"}}, nil), http.StatusSeeOther)
	if wk, _ := c.store.GetSubmission(mustID(t, id), c.sam.ID); wk.Body != "My answer, with an example." || wk.Status != store.WorkComplete {
		t.Fatalf("work after complete: %+v", wk)
	}
	expect(t, s.get("/"), http.StatusOK, "Nothing to do right now")
	expect(t, s.get(classPath+"/assignments"), http.StatusOK, "Read and respond", "Complete")
	expect(t, s.get("/assignments"), http.StatusNotFound)

	// The calendar feed has the due date.
	feeds := s.get("/calendar/subscribe")
	tok := regexp.MustCompile(`/ical/([A-Za-z0-9_-]+)\.ics`).FindStringSubmatch(feeds.Body)[1]
	expect(t, s.get("/ical/"+tok+".ics"), http.StatusOK, "UID:due-"+id+"@taper", "SUMMARY:Due: Read and respond")

	// Scholars can't change assignments.
	expect(t, s.get(path+"/edit"), http.StatusForbidden)
	expect(t, s.postForm(path+"/delete", nil), http.StatusForbidden)

	// Backups include the files.
	a := c.signedIn("admin", "admin-password")
	bk := a.get("/admin/backup?files=1")
	expect(t, bk, http.StatusOK)
	names := tarNames(t, bk.Body)
	if !names["taper.db"] || len(names) != 3 {
		t.Fatalf("backup holds %v", names)
	}

	// Deleting the assignment removes its files from disk.
	dir := c.srv.filesDir()
	if n := countFiles(t, dir); n != 2 {
		t.Fatalf("%d files stored, want 2", n)
	}
	m.get(path + "/edit")
	expectRedirect(t, m.postForm(path+"/delete", nil), classPath+"/assignments")
	if n := countFiles(t, dir); n != 0 {
		t.Fatalf("%d files left after deleting", n)
	}
	expect(t, s.get(path), http.StatusNotFound)
}

func TestAssignmentPublishLater(t *testing.T) {
	c := newCalEnv(t)
	classPath := fmt.Sprintf("/classes/%d", c.class.ID)
	m := c.signedIn("mia", "mentor-password")
	id := assignmentID(t, m.postFiles(classPath+"/assignments/new", url.Values{"title": {"Next week"}, "publish": {"later"},
		"publish_date": {c.tomorrow}, "publish_time": {"08:00"}}, nil))
	expect(t, m.get("/assignments/"+id), http.StatusOK, "Scholars see it from")
	s := c.signedIn("sam", "scholar-password")
	expect(t, s.get("/assignments/"+id), http.StatusNotFound)

	// Publishing it now shows it.
	m.get("/assignments/" + id + "/edit")
	expectRedirect(t, m.postFiles("/assignments/"+id+"/edit", url.Values{"title": {"Next week"}, "publish": {"now"}}, nil), "/assignments/"+id)
	expect(t, s.get("/assignments/"+id), http.StatusOK, "Next week", "No due date")

	// Paper work: no writing box, no files, just Turn in.
	page := s.get("/assignments/" + id)
	if strings.Contains(page.Body, `name="body"`) || strings.Contains(page.Body, `type="file"`) {
		t.Fatal("work online and files were off")
	}
	expectRedirect(t, s.postFiles("/assignments/"+id+"/work", url.Values{"action": {"turn_in"}}, nil), "/assignments/"+id+"#work")
	expect(t, s.get("/assignments/"+id), http.StatusOK, "Turned in")
}

func TestUploadLimits(t *testing.T) {
	c := newCalEnv(t)
	classPath := fmt.Sprintf("/classes/%d", c.class.ID)
	m := c.signedIn("mia", "mentor-password")
	big := strings.Repeat("x", maxFile+1)
	r := m.postFiles(classPath+"/assignments/new", url.Values{"title": {"Big"}, "publish": {"now"}}, map[string]string{"big.bin": big})
	expectRedirect(t, r, "/assignments/1/edit")
	expect(t, m.get("/assignments/1/edit"), http.StatusOK, "The assignment is saved, but not all its files", "big.bin is too big")
	if n := countFiles(t, c.srv.filesDir()); n != 0 {
		t.Fatalf("%d files stored", n)
	}
}

func TestCleanFileName(t *testing.T) {
	for in, want := range map[string]string{
		`C:\Users\sam\My Essay.docx`: "My Essay.docx",
		"../../etc/passwd":           "passwd",
		"a\"b<c>.txt":                "abc.txt",
		"..":                         "file",
		"":                           "file",
		"line\nbreak.pdf":            "linebreak.pdf",
	} {
		if got := cleanFileName(in); got != want {
			t.Errorf("cleanFileName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("a", 300) + ".pdf"
	if got := cleanFileName(long); len([]rune(got)) != 120 || !strings.HasSuffix(got, ".pdf") {
		t.Errorf("long name: %q", got)
	}
}

func tarNames(t *testing.T, body string) map[string]bool {
	t.Helper()
	gz, err := gzip.NewReader(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[h.Name] = true
	}
	return names
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			n++
		}
		return nil
	})
	return n
}

func TestReviewUndo(t *testing.T) {
	c := newCalEnv(t)
	classPath := fmt.Sprintf("/classes/%d", c.class.ID)
	m := c.signedIn("mia", "mentor-password")
	id := assignmentID(t, m.postFiles(classPath+"/assignments/new", url.Values{"title": {"Essay"}, "publish": {"now"}, "work_online": {"1"}}, nil))
	path := "/assignments/" + id
	review := fmt.Sprintf("%s/work/%d", path, c.sam.ID)
	s := c.signedIn("sam", "scholar-password")
	s.get(path)
	expectRedirect(t, s.postFiles(path+"/work", url.Values{"body": {"Draft one"}, "action": {"turn_in"}}, nil), path+"#work")
	status := func() *store.Submission {
		w, err := c.store.GetSubmission(mustID(t, id), c.sam.ID)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	turnedIn := status().TurnedInAt

	// Undo complete: back to turned in, keeping when it was turned in.
	expect(t, m.get(review), http.StatusOK, "Return to scholar")
	if strings.Contains(m.last, "Undo complete") {
		t.Fatal("nothing to undo before it's complete")
	}
	expectRedirect(t, m.postForm(review, url.Values{"decision": {"complete"}}), path+"#roster")
	expect(t, m.get(review), http.StatusOK, "Undo complete", "Return to scholar")
	expectRedirect(t, m.postForm(review, url.Values{"decision": {"undo_complete"}}), review)
	if w := status(); w.Status != store.WorkTurnedIn || w.TurnedInAt != turnedIn {
		t.Fatalf("after undo complete: %+v", w)
	}
	expect(t, m.get(review), http.StatusOK, "no longer marked complete", "Complete undone, back to turned in by Mia Mentor")

	// Return to scholar: not turned in, and they can change it again.
	expectRedirect(t, m.postForm(review, url.Values{"decision": {"unsubmit"}}), review)
	if w := status(); w.Status != store.WorkDraft || w.Body != "Draft one" {
		t.Fatalf("after returning: %+v", w)
	}
	expect(t, s.get(path), http.StatusOK, "Not turned in", "Turn in", "Returned, not turned in by Mia Mentor")
	expectRedirect(t, s.postFiles(path+"/work", url.Values{"body": {"Draft two"}, "action": {"save"}}, nil), path+"#work")
	if status().Body != "Draft two" {
		t.Fatal("returned work should be editable")
	}

	// Nothing to undo now; scholars and other mentors can't.
	expectRedirect(t, m.postForm(review, url.Values{"decision": {"unsubmit"}}), review)
	expect(t, m.get(review), http.StatusOK, "doesn't apply to this work any more")
	expect(t, s.postForm(review, url.Values{"decision": {"unsubmit"}}), http.StatusNotFound)
	c.addUser("otto", "Otto", store.RoleMentor, "mentor-password", false)
	expect(t, c.signedIn("otto", "mentor-password").postForm(review, url.Values{"decision": {"unsubmit"}}), http.StatusNotFound)

	// Work marked complete without being turned in goes back to not turned in.
	id2 := assignmentID(t, m.postFiles(classPath+"/assignments/new", url.Values{"title": {"Done in class"}, "publish": {"now"}}, nil))
	review2 := fmt.Sprintf("/assignments/%s/work/%d", id2, c.sam.ID)
	expectRedirect(t, m.postForm(review2, url.Values{"decision": {"complete"}}), "/assignments/"+id2+"#roster")
	expect(t, m.get(review2), http.StatusOK, "Undo complete")
	if strings.Contains(m.last, "Return to scholar") {
		t.Fatal("it was never turned in, so there's nothing to return")
	}
	expectRedirect(t, m.postForm(review2, url.Values{"decision": {"undo_complete"}}), review2)
	if w, _ := c.store.GetSubmission(mustID(t, id2), c.sam.ID); w.Status != store.WorkDraft {
		t.Fatalf("undo complete without turning in: %+v", w)
	}
}
