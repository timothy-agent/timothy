package builtin

import "github.com/SumonMSelim/timothy/internal/brain/tools"

// Catalog returns every builtin tool built with zero dependencies, for
// describing the tool surface (cmd/manifest). Its tools are never run:
// Execute on a nil dependency fails.
func Catalog() []*tools.Tool {
	return []*tools.Tool{
		Calculator(),
		ConvertCurrency(nil),
		ConvertTime(),
		CreateMission(nil, nil, nil),
		CurrentTime(nil, nil),
		Deliver(nil, nil),
		FollowupMission(nil, nil),
		GeneratePDF(nil),
		GetMission(nil, nil),
		KBRead(nil),
		KBSearch(nil),
		ListMissions(nil),
		PushMissionBranch(nil, nil, nil),
		Remember(nil),
		RetrieveOutput(nil),
		SearchMemory(nil),
		ShareFile(ShareFileConfig{}),
		Shell(ShellConfig{}),
		WebFetch(WebFetchConfig{}),
		WebSearch(""),
		WriteFile(WriteFileConfig{}),
		WritingSamples(nil, nil),
	}
}
