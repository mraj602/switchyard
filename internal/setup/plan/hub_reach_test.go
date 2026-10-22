package plan_test

import (
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/switchyard/internal/setup/model"
)

func connects(p model.Plan) []model.Action {
	var out []model.Action
	for _, a := range p.Actions {
		if a.Op == model.OpConnect {
			out = append(out, a)
		}
	}
	return out
}

func TestPlanHubPull(t *testing.T) {
	st := detecttest.Laptop(t).State()
	p := mustPlan(t, st, model.Choices{Hub: model.HubChoice{Mode: model.HubPull}})
	cs := connects(p)
	if len(cs) != 1 || cs[0].Reach != model.HubPull || cs[0].URL != "" || !strings.Contains(cs[0].Note, "pulled over SSH") {
		t.Fatalf("connect actions %+v", cs)
	}
	for _, n := range p.Needs {
		if n.Kind == "hub-token" {
			t.Fatal("pull asks for a token")
		}
	}
	// Already pulled with the timer on: nothing to do.
	st.Hub.Reach, st.Hub.TimerActive = model.HubPull, true
	p = mustPlan(t, st, model.Choices{Hub: model.HubChoice{Mode: model.HubPull}})
	if len(connects(p)) != 0 {
		t.Fatalf("rerun plans a connect: %+v", connects(p))
	}
}

func TestPlanHubJoinDialReach(t *testing.T) {
	st := detecttest.Laptop(t).State()
	join := model.HubChoice{Mode: model.HubJoin, URL: "https://hub.example"}
	plain := mustPlan(t, st, model.Choices{Hub: join, TokenStdin: true})
	pc := connects(plain)
	if len(pc) != 1 || pc[0].Dial != "" || pc[0].Reach != "" {
		t.Fatalf("plain join %+v", pc)
	}
	fw := mustPlan(t, st, model.Choices{Hub: join, CodeStdin: true, Dial: "127.0.0.1:47830"})
	fc := connects(fw)
	if len(fc) != 1 || fc[0].Dial != "127.0.0.1:47830" || fc[0].Reach != "forward" || !strings.Contains(fc[0].Note, "pairing code") {
		t.Fatalf("forward join %+v", fc)
	}
	if fw.PlanID == plain.PlanID {
		t.Fatal("the dial does not change the plan id")
	}

	// Connected the same way already: keep. A different dial: connect again.
	u := "https://hub.example"
	st.Hub = model.HubState{URL: &u, Healthy: true, TokenPresent: true, Reach: "forward", Dial: "127.0.0.1:47830"}
	if p := mustPlan(t, st, model.Choices{Hub: join, CodeStdin: true, Dial: "127.0.0.1:47830"}); len(connects(p)) != 0 {
		t.Fatalf("same link replanned: %+v", connects(p))
	}
	if p := mustPlan(t, st, model.Choices{Hub: join, CodeStdin: true, Dial: "127.0.0.1:47831"}); len(connects(p)) != 1 {
		t.Fatal("a new dial is not planned")
	}
	if p := mustPlan(t, st, model.Choices{Hub: join, CodeStdin: true}); len(connects(p)) != 1 {
		t.Fatal("leaving forward is not planned")
	}
}
