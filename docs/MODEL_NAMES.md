# Model naming — how the GLBs get human-readable names

Exported files look like `CHAR_black_widow_0x13ECB2D8.glb` instead of just
`CHAR_0x13ECB2D8.glb`. This document explains where those names come from and
how to regenerate or extend the mapping.

## The key fact: sprite dictID == server modelid

Every ESF object carries a 32-bit `DictID` (see
[FORMATS.md](FORMATS.md#esf--object-tree-viobjfile)). For sprites, this ID is
not just a file-internal reference — **it is the same value the EQOA game
server used as `modelid`** when telling clients what to render. The server
databases preserved by the community therefore double as a name directory
for the client assets.

Verification: the ReturnHome server project's `charactermodel` table lists 20
player models as `(sex, modelid, race)` rows. Interpreting each signed-int32
`modelid` as an unsigned dictID, all 20 exist in the beta `CHAR.ESF` export —
20/20 — and render as the expected race/sex when viewed. The race enum
(`Race.cs`): 0 Human, 1 Elf, 2 Dark Elf, 3 Gnome, 4 Dwarf, 5 Troll,
6 Barbarian, 7 Halfling, 8 Erudite, 9 Ogre.

## NPC names: majority vote over retail spawn captures

The server's `npcs` table (in `EQOA_Master.sql`) contains rows captured from
retail-era gameplay: each spawn has an `npc_name` ("a black widow",
"Guard Hamon") and the `modelid` it rendered with. One model is used by many
differently-named NPCs, so the pipeline takes the **most common name per
modelid**, strips leading articles ("a", "an", "the"), and sanitizes to a
filename-safe token.

Cross-validation: creatures we identified purely visually from rendered
animations were later matched against their recovered names — the 8-legged
model was `black_widow`, the claws-and-stinger model `whiptail_scorpion`,
the flat rearing model `copperhead`, the tree-creatures
`Treant_Defender`/`withered`/`hollow`, the rider-on-hound `Houndsman_Prek`.
Every one matched.

Coverage on the Frontiers beta `CHAR.ESF`: 404 of 1035 sprites named. The
remainder are beta-only assets or models no captured retail NPC spawned with;
they keep the plain `CHAR_0x<ID>.glb` name.

## Source of truth

```
cmd/model_names.json ──go:embed──► reader convert → CHAR_<Name>_0x<ID>.glb
cmd/zone_tile_names.json ──go:embed──► reader convert → zone_87_Qeynos.glb
```

The two JSON manifests in `cmd/` are **version-controlled data, edited
directly in this repository** — no external databases, downloads, or
generation scripts are involved in building or using the tool.  To add or
correct a name: edit the JSON, rebuild, done.  Contributions welcome; keys
are uppercase hex dictIDs without `0x`.

- `cmd/model_names.json` + `cmd/model_names.go` — model names, looked up at
  export time in `generateGLB` (`cmd/convert.go`).
- `cmd/zone_tile_names.json` + `cmd/zone_names.go` — per-world zone name
  lists and 2000-unit tile grids, looked up when zone GLBs are written.

Provenance (how the initial data was recovered, for the record): model names
were extracted from the EQOAGameServer community database — the 20 player
models from the `charactermodel` table, NPC names majority-voted per modelid
over retail spawn captures in the `npcs` table (parsing the `INSERT`
statements with a quote-aware tokenizer, stripping leading articles).  Zone
tile names came from the eqoa.live map project's published grid data.

## Conventions

- The dictID stays in the filename (`CHAR_black_widow_0x13ECB2D8.glb`) —
  names are derived data and can collide or change; the ID is the identity.
- Multiple dictIDs can share one name (model variants); that is expected.
- glTF *animation* names are a separate mechanism — they come from the
  AnimationState ID table, not from this manifest (see
  [ANIMATION.md](ANIMATION.md#4-upperlower-body-pairs-and-layering)).
