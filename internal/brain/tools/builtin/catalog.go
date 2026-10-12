package builtin

import "github.com/SumonMSelim/timothy/internal/brain/tools"

// Catalog returns every builtin tool built with zero dependencies, for
// describing the tool surface (cmd/manifest). Its tools are never run:
// Execute on a nil dependency fails.
func Catalog() []*tools.Tool {
	return []*tools.Tool{
		Calculator(),
		ConvertCurrency(nil),
		CreateAutomation(nil),
		DeleteAutomation(nil),
		ConvertTime(),
		CreateMission(nil, nil, nil),
		CurrentTime(nil, nil),
		Deliver(nil, nil),
		ForkMission(nil, nil),
		GeneratePDF(nil),
		ReadMission(nil, nil),
		KBRead(nil),
		KBSearch(nil),
		ListAutomations(nil),
		ListMissions(nil),
		PushMissionBranch(nil, nil, nil),
		ReadAutomation(nil),
		Remember(nil),
		RetrieveOutput(nil),
		SearchMemory(nil),
		UpdateAutomation(nil),
		ShareFile(ShareFileConfig{}),
		Shell(ShellConfig{}),
		TimothyHelp(TimothyHelpConfig{}),
		WebFetch(WebFetchConfig{}),
		WebSearch(""),
		WriteFile(WriteFileConfig{}),
		WritingSamples(nil, nil),
	}
}
