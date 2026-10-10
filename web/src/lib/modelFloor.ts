// belowMissionFloor mirrors missions.BelowModelFloor (runner.go): a
// model containing any floor substring, case-insensitively, can chat
// but cannot run missions.
export function belowMissionFloor(model: string, floor: readonly string[] | null | undefined): boolean {
  const m = model.toLowerCase()
  return (floor ?? []).some((deny) => deny !== '' && m.includes(deny.toLowerCase()))
}
