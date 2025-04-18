# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- Correct golangci-lint config.

## [0.3.0] - 2025-04-10

### Added

- Add `s3.http` option to enable HTTP instead of HTTPS for pulling images from S3.
- Add `vsphere.pullFromURL` option to pull images directly from a URL instead of uploading them to vSphere.

## [0.2.1] - 2025-04-01

### Changed

- Support exotic characters in password with `url.UserPassword`.

## [0.2.0] - 2025-03-31

### Changed

- Exclude all non vSphere releases.

### Added

- Add `imagesuffix` to location field to set a suffix on the uploaded VM template name.

## [0.1.0] - 2025-03-27



[Unreleased]: https://github.com/giantswarm/image-distribution-operator/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/giantswarm/image-distribution-operator/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/giantswarm/image-distribution-operator/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/giantswarm/image-distribution-operator/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/giantswarm/image-distribution-operator/releases/tag/v0.1.0
