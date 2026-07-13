# EQOA Armor / Character Texture Mapping — RE status & resume plan

**Status (2026-07-13):** architecture *fully confirmed* from the client's own C++
symbols; concrete per-texture labels are **blocked on a live memory read** because
the lookup tables are runtime-populated. This doc is the pick-up-cold plan.

## The question

CHARCUST.ESF (`data2\charcust.csf`) = **8 SimpleSprites** (helmet/head geometry — the
8 GLBs) + **137 standalone Surfaces** (body-armor SKIN textures, mostly 64×64). Body
armor in EQOA is **not geometry** — it is a *texture swap* onto the shared body mesh's
material slots. Items map textures onto item geometry the same way. We extract the 137
skins today as anonymous PNGs; the goal is to **label each** (which armor set / body
slot / race) so we can name them and optionally emit armored-character GLBs.

## Confirmed mechanism — class `VICSpriteCust`

The client has an SN-Systems runtime symbol block (mangled C++ names). `VICSpriteCust`
is the body texture-swap engine. Body = one `VISkinSprite` with **5 material slots**
(`VICSpriteTextSlot`, proven by the applier loop `iVar8 = 0..4`).

Selection methods (addresses are from the **`slus_280.28_snapshot_data.elf` symbol
table** — see skew warning below):

| Method | Addr (symtab) | Role |
|---|---|---|
| `GetArmorSetTexture(VICSpriteRace, VICSpriteArmorSet, VICSpriteTextSlot)` | `0x004084e8` | body-armor skin per slot |
| `GetRobeTexture(slot)` | `0x00408458` | robe overlay |
| `GetHairTexture(i)` | `0x00408410` | hair |
| `GetFaceTexture(i,i,race)` | `0x00428840` | face (from separate CHARFACE) |
| `GetHelm(VICSpriteArmorSet)` | `0x004bdf50` | → the 8 SimpleSprite helmet meshes |
| `GetTintColor(VICSpriteTint)` | `0x00408480` | RGBA tint (not a texture) |
| `SetResources(VIRaster, VIDictionary)` | `0x004afa08` | **builds the tables from the loaded charcust dictionary** |
| `SetMaterialPal(VISkinSprite,i,VIRaster,VIDictionary)` | `0x00458458` | binds a material palette to the body |

Applier `FUN_004021b0` (Ghidra) loops the 5 slots; per slot it fetches a robe texture
and the armor-set texture and binds them via `FUN_003de380` / `FUN_003de428`.

Static data tables (symtab addrs): `$Race`@`0x4c6c60` (184 B), `$Hair`@`0x4c6d18`
(352 B), `$Robe`@`0x4c6e78` (32 B), `$Helm`@`0x4c6e98` (32 B), `$Tints` (14 × RGBA @
`0x4afa08` region), plus a table-init ctor (symtab-named `$Armor`) @ `0x4072b8` that
**zeroes** `$Race/$Hair/$Robe` at startup.

DictID hash (for reference) = polynomial `h = h*0x83 + c` over the asset name
(`FUN_003ceb10`; verified: `EruditeMale` → `0x320C0B47`). **Charcust textures are
addressed by dictionary index, not name-hash**, so hashing item names finds nothing —
confirmed (0 matches) and a raw-DictID scan of the ELFs is also 0.

## Why static analysis stops here

1. **Tables are runtime-populated.** In *both* snapshot ELFs the `$Race/$Hair/$Robe/
   $Helm` tables are all-zero and `$Tints`/armor colors are the default `0xFF000000`.
   `SetResources` fills them only when a character's charcust data loads.
2. **3-way address skew.** Ghidra snapshot ≠ `_data.elf` symtab ≠ retail. E.g. the
   `$Race` writer is `~0x40842c` in the Ghidra image but the ctor is `0x4072b8` in the
   symtab; `SetResources` "`0x4afa08`" from the symtab is *data* in Ghidra ("No function
   at 0x004afa08"). So a static `SetResources` replay is fragile AND yields no values.

## Resume plan — live PINE read (ground truth)

The `pine_*` MCP tools are available. Procedure:

1. `pine_ping` / `pine_get_status` — confirm PCSX2 is connected and identify the build
   (retail vs beta) so table addresses are correct. **Re-derive the live addresses for
   that build** — do NOT assume the symtab addresses above; they are for the snapshot,
   and skew is proven. Locate `VICSpriteCust::SetResources` / the `$Race`,`$Hair`,
   `$Robe`,`$Helm` tables in the *running* image (find them via the same symbol block in
   the live binary, or by xrefs to the accessor cluster).
2. Get to **character-select or in-game** (charcust loaded) — needs a savestate or live
   session at that screen.
3. `pine_read_range` the `$Race/$Hair/$Robe/$Helm` tables now that they're populated;
   entries are **dictionary indices** into the loaded charcust dictionary.
4. Map dictionary index → **charcust.csf Surface order** (the 137, in file order from
   `eqonvert inspect --json CHARCUST.CSF`).
5. Join `VICSpriteArmorSet`/`VICSpriteTextSlot`/`VICSpriteRace` → armor-set / slot /
   race **names** via the server DB: `itempattern.patternfam` (armor set),
   `equipslot` (slot). See `EQOAGameServer/EQOA_Master.sql`.
6. Validate: pick a known in-game armor set, confirm the surface it maps to is the
   texture we'd expect.

Deliverable when unblocked: a table `surfaceIndex(0..136) → {race, armorSet, slot}` →
human name, feeding the `eqonvert` extractor to name the 137 PNGs and (optionally) emit
armored-character GLBs by binding a skin set to a body's 5 material slots.

## Fallback (no emulator)

Structural/heuristic labeling from charcust.csf surface order + dimensions + the DB
enumeration of armor sets/slots. Usable but **not exact** — prior heuristic passes on
this codebase have been wrong before, so treat as provisional only.

## Related

Memory: `project_armor_texture_swap`, `project_model_naming`, `project_client_game_logic`,
`project_char_garment_alpha`, `project_char_dup_blank`.
