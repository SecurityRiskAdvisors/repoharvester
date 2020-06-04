# repoharvester
Harvest email addresses from commit entries from Github -- faster

## Introduction
This application is the spiritual successor of githump (https://github.com/int0x80/githump).

It is a highly concurrent application to get the list of repositories for a user or org, and pull the emails from them.

It will output a unique list of emails found in all the repos, and a JSON with an email <--> repo mapping.

It is written in Go and is cross-platform, although it needs `git` binary installed to function.

_Git is much more performant than go libraries that perform git actions. Integration may happen, but not at this time._

```
$ repoharvester -h
Usage:
  repoharvester [OPTIONS] target-name

Resource Options (Required):
  -t, --type=[user|org|url]                  type of object to target
  -o, --org                                  alias to --type org
  -u, --user                                 alias to --type user
      --url                                  alias to --type url
      --size-filter=<size in kB>             repo size to filter (set 0 to disable) (default: 1000000)
      --no-fork                              filter out forked repos

Output Options (Required):
  -j, --json=output.json                     Output JSON file
  -f, --file=output.list                     Output flat file

Application Options:
  -v, --verbose                              Show verbose debug information
  -q, --quiet                                Show fewer messages
      --preserve-dir                         preserve working directory
  -w, --working-dir=<path_to_working_dir>    working dir path (should have space to store all repos) (default: Uses working directory)
  -g, --git-path=<path_to_git>               path to git (default: Uses system git)

Advanced Options:
      --workers=<int>                        numbers of workers to use (default: 20)
      --queue-size=<int>                     base size of the operating queue (default: 20)

Help Options:
  -h, --help                                 Show this help message

Arguments:
  target-name:                               The name of the user or org to faceprint
```

## Usage
- Download or install `git`.

- Download `repoharvester` from the releases page.

- Run repoharvester against a specific org
```
$ repoharvester -f output.list -j output.json -t org securityriskadvisors
```

- You can also target a user
```
$ repoharvester -f output.list -j output.json -t user <username>
```
- If using targetting Github Enterprise, you can also specify a URL.

The URL should be in the form of: `https://<host>/<type>/<id>/repos?per_page=100` for example `https://api.github.com/orgs/securityriskadvisors/repos?per_page=100`.



```
$ repoharvester -f output.list -j output.json -t url <url>
```
- Specify a working dir for larger orgs since the repositories have to be downloaded to be parsed.

_By default it will write to the OS working directory
```
$ repoharvester -w /opt/working_dir -f output.list -j output.json -t org securityriskadvisors
```

- You can also specify the location of the `git` binary if not in your $PATH
```
$ repoharvester -w /opt/working_dir -g /usr/bin/git -f output.list -j output.json -t org securityriskadvisors
```
- There is a size filter in place skipping repos that are > 1GB. Those repositories tend to be asset heavy and don't contain many commits. You can modify or remove this limit with the `--size-filter` parameter. You can disable the filter by setting it <=0.
```
$ repoharvester --size-filter=0 -f output.list -j output.json -t org securityriskadvisors
```
- You can also choose to ignore forked repos in the org/user
```
$ repoharvester --no-fork -f output.list -j output.json -t org securityriskadvisors
```

## Acknowledgments ##
- https://github.com/int0x80/githump

