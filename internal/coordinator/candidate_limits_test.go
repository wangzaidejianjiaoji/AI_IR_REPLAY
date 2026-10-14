package coordinator

import (
	"context"
	"fmt"
	"testing"
	"gvs-pr-agent/internal/agent"
)

func TestCandidateCapIsDisclosedAndOriginalFindingsPreserved(t *testing.T){
	var candidates []agent.Issue
	for i:=0;i<41;i++{candidates=append(candidates,agent.Issue{File:"a.go",Line:1,Problem:fmt.Sprintf("original-%d",i),Detail:"code evidence",Severity:"high"})}
	got,_,err:=NewConfirmer(&confirmModel{}).Confirm(context.Background(),ConfirmationInput{
		Diff:"diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n",Candidates:candidates,
	})
	if err!=nil{t.Fatal(err)}
	if got.UnconfirmedCount!=1||len(got.Issues)!=40{t.Fatalf("silent candidate truncation: %+v",got)}
	for _,issue:=range got.Issues{if issue.Detail!="code evidence"{t.Fatal("original evidence rewritten")}}
}

func TestDuplicateAndUnknownConfirmationIDFailClosed(t *testing.T){
	input:=ConfirmationInput{Diff:"diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n",Candidates:[]agent.Issue{{File:"a.go",Line:1,Problem:"p"}}}
	_,_,err:=NewConfirmer(protocolModel{content:`{"decisions":[{"candidate_id":"unknown","confirmed":true,"reason":"invented"}]}`}).Confirm(context.Background(),input)
	if err==nil{t.Fatal("unknown ID accepted")}
}
