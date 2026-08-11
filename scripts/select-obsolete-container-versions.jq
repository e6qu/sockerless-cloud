# GHCR can coalesce byte-identical tags from several releases into one
# indivisible package version. Treat releases connected through any shared
# package version as one atomic component. Keep the newest complete components
# only while the complete release count stays within the requested limit.

def release_name:
  select(type == "string")
  | ascii_downcase
  | capture("^(?<release>[0-9a-f]{12})(?:-(?:amd64|arm64))?$")?
  | .release;

def intersects($left; $right):
  any($left[]; . as $item | $right | index($item) != null);

def expand_component($groups):
  . as $members
  | reduce $groups[] as $group ($members;
      . as $expanded
      | if intersects($group; $expanded)
        then ($expanded + $group | unique)
        else $expanded
        end
    );

def release_component($release; $groups):
  [$release]
  | until(
      . as $members | expand_component($groups) == $members;
      expand_component($groups)
    );

. as $versions
| ([ $versions[].metadata.container.tags[]? | ascii_downcase ] | unique) as $all_tags
| ([ $all_tags[]
     | select(test("^[0-9a-f]{12}$"))
     | . as $release
     | select(
         ($all_tags | index($release + "-amd64")) != null
         and ($all_tags | index($release + "-arm64")) != null
       )
   ]) as $complete_releases
| ([ $versions[] as $version
     | {
         members: [
           ($version.metadata.container.tags // [])[]
           | release_name
         ] | unique,
         recognized: all(
           ($version.metadata.container.tags // [])[];
           test("^[0-9a-f]{12}(-(amd64|arm64))?$"; "i")
         )
       }
     | select(.members | length > 0)
   ]) as $version_groups
| ([ $versions[] as $version
     | ($version.metadata.container.tags // [])[]
     | ascii_downcase
     | select(test("^[0-9a-f]{12}$"))
     | {tag: ., created_at: $version.created_at}
   ] | unique_by(.tag)) as $release_records
| ([ $complete_releases[] as $release
     | (release_component($release; [$version_groups[].members])) as $members
     | {
         members: $members,
         created_at: ([
           $release_records[]
           | select(.tag as $tag | $members | index($tag) != null)
           | .created_at
         ] | max)
       }
   ]
   | unique_by(.members)
   | map(select(
       .members as $members
       | all(
           $members[];
           . as $release | $complete_releases | index($release) != null
         )
         and all(
           $version_groups[];
           . as $group
           | $group.recognized
             or (intersects($group.members; $members) | not)
         )
     ))
   | sort_by(.created_at)
   | reverse
  ) as $components
| (reduce $components[] as $component (
    {accepting: true, count: 0, members: []};
    if .accepting and (.count + ($component.members | length) <= $keep)
    then {
      accepting: true,
      count: (.count + ($component.members | length)),
      members: (.members + $component.members)
    }
    else .accepting = false
    end
  )) as $selected
| ($selected.members
   | map(., . + "-amd64", . + "-arm64")
   | unique
  ) as $keep_tags
| $versions[]
| . as $version
| ($version.metadata.container.tags // []) as $tags
| select(
    ($tags | length) == 0
    or all($tags[]; . as $tag | $keep_tags | index($tag) == null)
  )
| $version.id
