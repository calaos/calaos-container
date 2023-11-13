import git
import semver

repo = git.Repo(".")
tags = sorted(repo.tags, key=lambda t: t.commit.committed_datetime)
print(str(max(map(semver.VersionInfo.parse, map(str, tags)))))
