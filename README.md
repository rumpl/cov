# cov

Go code coverage by file.

The go tool `cover` can show the coverage by function or coverage in html mode but neither of those fit my need. This tool will show the coverage by file and show nice colors (green/yellow/red) to quickly see the state of your coverage.

## Intall

```
$ go get -u github.com/rumpl/cov
```

Or you can download the binary from the releases page.

## Usage

First you need to generate a cover file

```
$ go test -covermode=count -coverprofile=cover.out
```

Then you can simply:

```
$ cov cover.out
/Users/djordjelukic/dev/dep-sum/main.go	31.4%

Total:					31.4%
...
```
