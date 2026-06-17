package domain

import "testing"

func TestEnumValidity(t *testing.T) {
	valid := []interface{ Valid() bool }{
		EnvDev, EnvStaging, EnvProdLocked,
		ProtocolREST, ProtocolGRPC, ProtocolWS,
		CredPool, CredBootstrapSignup, CredLogin,
		LoginPerUser, LoginShared,
		LoadWeight, LoadRamp, LoadSpike, LoadSoak,
		RunLocal, RunDistributed,
		RunPending, RunRunning, RunCompleted, RunKilled, RunFailed,
		FindingThreshold, FindingContract, FindingMutation, FindingAvailability,
		SeverityCritical, SeverityWarning, SeverityInfo,
		RoleOperator, RoleViewer,
	}
	for _, v := range valid {
		if !v.Valid() {
			t.Errorf("%v should be valid", v)
		}
	}

	invalid := []interface{ Valid() bool }{
		EnvClass("prod"), Protocol("tcp"), CredentialStrategy("oauth"),
		LoginScope("global"),
		LoadStrategy("burst"), RunMode("cluster"), RunStatus("paused"),
		FindingCategory("perf"), Severity("fatal"), AccessRole("admin"),
	}
	for _, v := range invalid {
		if v.Valid() {
			t.Errorf("%v should be invalid", v)
		}
	}
}
