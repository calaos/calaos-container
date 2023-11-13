import git
import semver
import sys

if len(sys.argv) < 2:
    print("Please provide the path to the repository as an argument.")
    sys.exit(1)

repo = git.Repo(sys.argv[1])
tags = sorted(repo.tags, key=lambda t: t.commit.committed_datetime)
print(str(max(map(semver.VersionInfo.parse, map(str, tags)))))
