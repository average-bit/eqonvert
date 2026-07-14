# World DB (`eqonvert world`)

Builds a single navigable SQLite database of the whole game world — every zone's
actor placements in full 7-DOF, a zone-adjacency graph, and (optionally) the
server-side spawn/creature layer — organised macroscopic → micro:

```
region  →  zone  →  placement  →  model
                                    ↑
                        spawn (server) ┘
```

## Usage

```sh
# client geometry only
eqonvert world <iso | dir | file.esf> -o world.db

# + server spawns & creature names from the EQOAGameServer dump
eqonvert world EverQuest_Online_Adventures.iso -o world.db \
         --server EQOAGameServer/EQOA_Master.sql
```

Browse with any SQLite tool — [Datasette](https://datasette.io) auto-generates a
navigable web UI that follows the foreign keys (region → zone → placement →
model). Pure-Go SQLite (no cgo).

## Schema

| Table | Rows | Notes |
|-------|------|-------|
| `regions` | one per ESF/CSF file | TUNARIA, ODUS, RATHE… + AABB, counts |
| `zones` | `0x3000` objects | AABB, `center_*`, `grid_x/grid_z`, `border_mask` |
| `placements` | `0x6000` actors | **7-DOF**: `x,y,z / yaw,pitch,roll / scale` + RGBA, FK → model |
| `models` | distinct DictIDs | `name` (NULL = unknown), `kind` = prop/creature/sentinel/unknown |
| `zone_links` | adjacency graph | `cardinal` (N/NE/E…) + `bearing_deg` (0=N,90=E) + `gap` |
| `spawns` | server `npcs` | `world,zone,x,y,z,facing,model_dictid,size,hp,npc_level,npc_type,race` |
| view `v_unknown_models` | | unknown DictIDs by placement count |

**Coordinates** are EQOA world space: `x`=East, `y`=Height, `z`=North.
**Model↔spawn link:** `models.model_dictid == spawns.model_dictid`; the server
stores a DictID as a signed int32, so the importer applies `& 0xFFFFFFFF`.

## Zone adjacency (both human- and computer-navigable)

Zones tile a regular lattice (verified on TUNARIA: ~2000-unit cells). Each
non-empty zone gets integer `grid_x/grid_z`; neighbours are an explicit edge
list carrying the human **`cardinal`** label and the exact **`bearing_deg`**;
`border_mask` bits (N=1,E=2,S=4,W=8) mark world edges (no neighbour that way).

```sql
-- neighbours of a zone, by direction
SELECT neighbor_id, cardinal, round(bearing_deg) FROM zone_links
WHERE zone_id = 24 ORDER BY bearing_deg;

-- path between two zones (graph traversal in plain SQL)
WITH RECURSIVE hop(zone_id, path, depth) AS (
  SELECT :A, ','||:A||',', 0
  UNION ALL
  SELECT l.neighbor_id, hop.path||l.neighbor_id||',', depth+1
  FROM hop JOIN zone_links l ON l.zone_id = hop.zone_id
  WHERE instr(hop.path, ','||l.neighbor_id||',') = 0 AND depth < 64
) SELECT path FROM hop WHERE zone_id = :B ORDER BY depth LIMIT 1;
```

## Example queries

```sql
-- every unknown DictID and where it appears
SELECT * FROM v_unknown_models;

-- all placements of one model across the world
SELECT z.region_id, p.x,p.y,p.z FROM placements p JOIN zones z USING(zone_id)
WHERE p.model_dictid = :dictid;

-- creatures that spawn, by name and count
SELECT npc_name, count(*) FROM spawns WHERE npc_name!='' GROUP BY npc_name
ORDER BY 2 DESC;
```

## Notes / not yet done

- **Client-zone ↔ server-zone mapping**: `spawns.world/zone` are the server's
  own ids; correlating them to client `zone_id` (by AABB↔spawn-coord overlap) is
  a future step — until then join spawns↔placements via `model_dictid`, not zone.
- **Portals / zone-lines** (cross-region links) aren't extracted yet; they'd
  become `zone_links` rows with a `portal` kind.
- Empty container zones (no placements) are kept but have NULL grid coords and no
  adjacency.

See `DICTIDS.md` for the DictID model and `docs/` for the formats.
