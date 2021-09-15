![gopherfs logo-sm](https://raw.githubusercontent.com/gopherfs/fs/main/cover.png)

A set of io/fs filesystem abstractions and utilities for Go

[![GoDoc](https://godoc.org/github.com/gopherfs/fs?status.svg)](https://godoc.org/github.com/gopherfs/fs) 

Please :star: this project

# Overview

This package provides io/fs interfaces for:

- Cloud providers
- Memory storage
- Wrappers for the "os" package
- Utilities for merging io.FS packages
- A caching system with support for:
	- Redis
	- GroupCache
	- Disk cache

If you are looking to use a single group of interfaces to access any type of filesystem, look no further. This package brings the power of Go 1.16's io/fs package with new interfaces to allow for writable filesystems.

With these standard sets of interfaces we have expanded the reach of the standard library to cover several common sets of filesystems. In addition we provide a caching system allowing a cascade of cache fills to handle your file caching needs.

Below we will break down the packages and you can locate documentation within the GoDoc or the README in various packages.

## Packages breakdown
```
└── fs
    ├── io
	│   ├── cache
	│   │   ├── disk
	│   │   ├── groupcache
	│   │   │   └── peerpicker
	│   │   └── redis
	│   ├── cloud
	│   │   └── azure
	│   │       └── blob
	│   │           ├── auth
	│   │           └── blob.go
	│   ├── mem
	│   │   └── simple
	│   └── os
```

- `fs`: Additional interfaces to allow writeable filesystems and filesystem utility functions
- `fs/io/cache`:  Additional interfaces and helpers for our cache system
	- `disk`:  A disk based cache filesystem
	- `groupcache`:  A groupcache based filesystem
		- `peerpicker`: A multicast based peerpicker for groupcache (does not work in the cloud)
	- `redis`:  A Redis based filesystem
- `fs/io/cloud`: A collection of cloud provider filesystems
	- `azure`: A collection of Microsoft Azure filesystems
		- `blob`: A filesystem implementation based on Azure's Blob storage
- `fs/io/mem`: A collection of local memory based filesystems
	- `simple`: A memory filesystem that requires ASCII based file paths, supports RO Pearson hasing
- `fs/io/os`: A filesystem wrapper based around the "os" package

## Examples

The most complete examples will be in the GoDoc for individual packages. But here are some excerpts for a few use cases.

### Optimize embed.FS when not in debug mode

![Choices](https://media.giphy.com/media/3og0Iwmv38WmJBrYvS/giphy.gif?cid=790b7611a0bd571b48291d830cf91d39f94f0083c2a7040a&rid=giphy.gif&ct=g)

embed.FS is great. But what if you want to have readable JS for debug and compact code when in production?  What if you'd also like to take several embed.FS and merge into a single tree?

`Merge()` and our simple memory storage to the rescue:

```go
optimized := simple.New(simple.WithPearson())

err := Merge(
	optimized, 
	somePkg.Embeded, 
	"/js/", // Puts the content of the embed fs into a sub-directory
	WithTransform(
		func(name string, content []byte) ([]byte, error){
			// If we are in debug mode, we want unoptimized Javascript
			if debug {
				return content, nil
			}
			switch path.Ext(name){
			case "js":
				return optimizeJS(content)
			case "go":
				return nil, nil
			}
			return content, nil
		},
	),
)
if err != nil {
	// Do something
}
optimized.RO() // Locks this filesystem for readonly
```

### Access Redis as a filesystem

![Just Cause](https://media.giphy.com/media/5xaOcLGm3mKRQuDYCgU/giphy.gif?cid=ecf05e47jk4t434yvdq6szu7f6c8dte8jty31o7ozd4y8awx&rid=giphy.gif&ct=g)


One of the more popular caching systems around is Redis. Redis of course has a lot of options around it, but most use cases are simply as a filesystem. If this is your use, you can gain
access to Redis using our `fs/io/cache/redis` implementation.

Here we simply create a connection to our local Redis cache, set a 5 minute expiration time on all files and then write a file.

```go
redisFS, err := redis.New(
	redis.Args{Addr: "127.0.0.1:6379"},
	// This causes all files to exire in 5 minutes.
	// You can write a complex ruleset to handle different files at
	// different rates.
	redis.WithWriteFileOFOptions(
		regexp.MustCompile(`.*`),
		redis.ExpireFiles(5 * time.Minute),
	),
)
if err != nil {
	// Do something
}

if err := redisFS.WriteFile("gopher.jpg", gopherBytes, 0644); err != nil {
	// Do something
}
```

### Build a Cascading Cache

![Need for speed](https://c.tenor.com/KgS_y6_wgF0AAAAd/road-runner-run.gif)

Here we are going to build a cascading cache system. The goal is to have multiple layers of cache to look at before finally going to the source. This code will:

- Pull from a groupcache first
- Try a disk cache second
- Pull from Azure's Blob Storage as the final resort

**Note**: This example uses a peerpicker for groupcache that will not work on major cloud providers, as they block broadcast and local multicast packets. You would need your own peerpicker to work for your cloud vendor.

```go
// This sets up a filesystem accessing Azure's Blob storage. This is where
// our permanent location for files will be located.
blobStore, err := blob.NewFS("account", "container", *cred)
if err != nil {
	// Do something
}

// A new peerpicker that broadcasts on port 7586 to find new peers.
picker, err := peerpicker.New(7586)
if err != nil {
	// Do something
}

// A groupcache that our app uses to find cached entries.
gc, err := groupcache.New(picker)
if err != nil {
	// Do something
}

// A disk cache for when the groupcache doesn't have the data.
diskFS, err := disk.New(
	"", 
	disk.WithExpireCheck(1 * time.Minute), 
	disk.WithExpireFiles(30 * time.Minute),
)
if err != nil {
	// Do something
}

// Creates our diskCache that looks at our disk for a file and if it
// cannot find it, pulls from blob storage.
diskCache, err := cache.New(diskFS, blobStore)
if err != nil {
	// Do something
}

// Creates our cascader that will search the groupcache first, then
// search the disk cache and finally will pull from Azure Blob storage.
cascacder, err := cache.New(gc, diskCache)
if err != nil {
	// Do something
}

// This reads a file. Since this is our first read of this file, it will
// come from Azure Blob storage and back fill our caches.
b, err := cascacder.ReadFile("/path/to/file")
if err != nil {
	// Do something
}
```

## Contributions

This project is open to contributions.  The best way to contribute:

1. Open a feature/bug request for the feature
2. After a brief discusssion, fork the repo
3. Commit your changes
4. Create a Pull Request

\#1 and \#2 simple prevents any time wasted by for things that might not be within the scope for this project or to make sure a bug solution is the right solution.

We are looking for contributors to:

- Support Azure Append and Page Blobs (we already support block blobs)
- Support GCP Blob storage
- Support GCP Filestore
- Support AWS S3
- Support AWS Elastic storage
- Support SFTP


## Alternatives

I should point out that there is already a great package for filesystem abstractions [Afero](https://github.com/spf13/afero).  While I've never used it, spf13 is the author of several great packages and it looks like it has great support for several different filesystem types.

**So why gopherfs?**  When I started writing this I was simply interested in trying to take advantage of io/fs.  I saw Afero after I had written a couple of filesystems and it did not have io/fs support. 

Afero was also geared towards its own method of abstraction that was built long before io/fs was a twinkle in the Go authors' eyes. 

Most of my services don't need complicated file permissions that afero provides. For my use cases, the service is access control and has full rights to the file system.

I find Afero more complicated to use for my use cases and it doesn't have support for cloud provider filesystems (though you could write one). 

If you need to support more complicated setups, I would use Aferno.  I expect I might add wrappers around some of its filesytems at some point in the future.