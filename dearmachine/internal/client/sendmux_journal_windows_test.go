package client

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dearmachine/dearmachine/internal/hostos"
	"golang.org/x/sys/windows"
)

func TestWindowsSendmuxJournalSurvivesWriterTermination(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "submission 雪 # %")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsSendmuxCrashHelper$")
	cmd.Env = append(os.Environ(), "DM_SENDMUX_CRASH_DIR="+directory)
	cmd.SysProcAttr = hostos.Detached()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer cmd.Process.Kill()
	ready := filepath.Join(directory, "ready")
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("writer did not commit its uncertain submission")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	s := &sendmuxJMAPSender{journalDir: directory}
	j, unlock, err := s.openSubmissionJournal(context.Background(), "reply", "hash")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if !j.EmailAttempted || !j.SubmissionAttempted || j.EmailID != "email-before-crash" {
		t.Fatal("committed uncertain-send record was lost")
	}
	j.SubmissionID = "recovered-submission"
	if err := j.save(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsSendmuxCrashHelper(t *testing.T) {
	directory := os.Getenv("DM_SENDMUX_CRASH_DIR")
	if directory == "" {
		return
	}
	s := &sendmuxJMAPSender{journalDir: directory}
	j, unlock, err := s.openSubmissionJournal(context.Background(), "reply", "hash")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	j.EmailAttempted, j.SubmissionAttempted, j.EmailID = true, true, "email-before-crash"
	if err := j.save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "ready"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Minute)
}

func TestWindowsSendmuxJournalImportsLegacyAndFailsClosed(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "submissions")
	if err := privateJournalDirectory(directory); err != nil {
		t.Fatal(err)
	}
	legacy := sendmuxSubmissionJournal{Fingerprint: "hash", EmailAttempted: true, SubmissionAttempted: true}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(directory, "reply.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	s := &sendmuxJMAPSender{journalDir: directory}
	j, unlock, err := s.openSubmissionJournal(context.Background(), "reply", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if !j.SubmissionAttempted {
		t.Fatal("legacy uncertain submission was discarded")
	}
	unlock()
	if _, _, err := s.openSubmissionJournal(context.Background(), "reply", "different-envelope"); err == nil {
		t.Fatal("changed-envelope idempotency accepted")
	}
	if err := os.WriteFile(filepath.Join(directory, "reply.sqlite"), []byte("broken database"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.openSubmissionJournal(context.Background(), "reply", "hash"); err == nil {
		t.Fatal("corrupt journal must not fall back to an older JSON record")
	}
}

func TestWindowsSendmuxJournalRejectsPublicState(t *testing.T) {
	for _, name := range []string{"directory", "reply.sqlite", "reply.sqlite-journal", "reply.json", "reply.json.lock"} {
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "submissions")
			if err := privateJournalDirectory(directory); err != nil {
				t.Fatal(err)
			}
			path := directory
			if name != "directory" {
				path = filepath.Join(directory, name)
				if err := os.WriteFile(path, []byte("must remain unchanged"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				t.Fatal(err)
			}
			sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FR;;;WD)")
			if err != nil {
				t.Fatal(err)
			}
			acl, _, err := sd.DACL()
			if err != nil {
				t.Fatal(err)
			}
			if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
				t.Fatal(err)
			}
			s := &sendmuxJMAPSender{journalDir: directory}
			if _, unlock, err := s.openSubmissionJournal(context.Background(), "reply", "hash"); err == nil {
				unlock()
				t.Fatal("public journal state was accepted")
			}
			info, err := os.Stat(path)
			if err != nil || hostos.Private(path, info, 0077) {
				t.Fatal("unsafe state was modified instead of rejected")
			}
		})
	}
}

func TestWindowsSendmuxJournalSerializesReply(t *testing.T) {
	s := &sendmuxJMAPSender{journalDir: filepath.Join(t.TempDir(), "submissions")}
	j, unlock, err := s.openSubmissionJournal(context.Background(), "reply", "hash")
	if err != nil {
		t.Fatal(err)
	}
	j.SubmissionAttempted = true
	if err := j.save(); err != nil {
		unlock()
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, secondUnlock, err := s.openSubmissionJournal(ctx, "reply", "hash")
	if err == nil {
		secondUnlock()
		unlock()
		t.Fatal("second provider call could bypass the reply lock")
	}
	unlock()
	j, unlock, err = s.openSubmissionJournal(context.Background(), "reply", "hash")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if !j.SubmissionAttempted {
		t.Fatal("successor did not see the committed attempt")
	}
}

// This two-phase fixture is invoked by the disposable-VM power-cycle gate.
// The ordinary suite must never leave a writer running or reset a machine.
func TestWindowsSendmuxPowerCycleFixture(t *testing.T) {
	directory := os.Getenv("DM_SENDMUX_POWERCYCLE_DIRECTORY")
	phase := os.Getenv("DM_SENDMUX_POWERCYCLE_PHASE")
	if directory == "" && phase == "" {
		t.Skip("requires the explicit disposable-VM power-cycle gate")
	}
	if !filepath.IsAbs(directory) || (phase != "write" && phase != "verify") {
		t.Fatal("an absolute fixture directory and write or verify phase are required")
	}
	s := &sendmuxJMAPSender{journalDir: directory}
	j, unlock, err := s.openSubmissionJournal(context.Background(), "power-cycle", "synthetic-reply")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if phase == "verify" {
		if !j.EmailAttempted || !j.SubmissionAttempted || j.EmailID != "email-before-reset" || j.SubmissionID != "" {
			t.Fatal("uncertain submission was not preserved across the VM power cycle")
		}
		return
	}
	if j.EmailAttempted || j.SubmissionAttempted {
		t.Fatal("writer requires a fresh fixture directory")
	}
	j.EmailAttempted, j.SubmissionAttempted, j.EmailID = true, true, "email-before-reset"
	if err := j.save(); err != nil {
		t.Fatal(err)
	}
	t.Log("POWERCYCLE_READY: uncertain submission committed; reset only the disposable guest")
	time.Sleep(10 * time.Minute)
	t.Fatal("the disposable VM was not reset during the fixture window")
}
