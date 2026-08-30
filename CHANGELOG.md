# Changelog

## [0.29.0](https://github.com/e6qu/sockerless-cloud/compare/v0.28.3...v0.29.0) (2026-08-30)


### Features

* **specs:** resolve composed routes so the surface tables show the whole surface ([#107](https://github.com/e6qu/sockerless-cloud/issues/107)) ([cc5669b](https://github.com/e6qu/sockerless-cloud/commit/cc5669bd01e8d2515e8be39c06ac80f008e67b9d))

## [0.28.3](https://github.com/e6qu/sockerless-cloud/compare/v0.28.2...v0.28.3) (2026-08-30)


### Bug Fixes

* preserve Amazon ECS rollout and Google monitoring auth state ([#105](https://github.com/e6qu/sockerless-cloud/issues/105)) ([5090491](https://github.com/e6qu/sockerless-cloud/commit/50904919a1ca26bbc36dca1611a0766953ff8f9a))

## [0.28.2](https://github.com/e6qu/sockerless-cloud/compare/v0.28.1...v0.28.2) (2026-08-29)


### Bug Fixes

* **specs:** compare each pin against the commit that touched its file, and hold a served method to a route that names it ([#103](https://github.com/e6qu/sockerless-cloud/issues/103)) ([386eba3](https://github.com/e6qu/sockerless-cloud/commit/386eba3d4ab01c4fc284228f354662f368a1ec0d))

## [0.28.1](https://github.com/e6qu/sockerless-cloud/compare/v0.28.0...v0.28.1) (2026-08-28)


### Bug Fixes

* **aws:** authorize a create against its resource type, and repair the installable build ([#101](https://github.com/e6qu/sockerless-cloud/issues/101)) ([465bf2b](https://github.com/e6qu/sockerless-cloud/commit/465bf2bee57ef566bfbf22cc99af1624d13f54ec))

## [0.28.0](https://github.com/e6qu/sockerless-cloud/compare/v0.27.0...v0.28.0) (2026-08-28)


### Features

* **sim:** serve four more document tails across Google Cloud and Azure ([#99](https://github.com/e6qu/sockerless-cloud/issues/99)) ([2834ff5](https://github.com/e6qu/sockerless-cloud/commit/2834ff5ae6335e12d647e42db02283d420c6c995))

## [0.27.0](https://github.com/e6qu/sockerless-cloud/compare/v0.26.0...v0.27.0) (2026-08-28)


### Features

* **gcp:** serve Firestore's document custom methods, and complete application observation ([#97](https://github.com/e6qu/sockerless-cloud/issues/97)) ([e8473d7](https://github.com/e6qu/sockerless-cloud/commit/e8473d7028fcd4477cb0215090402ecd864f2a02))

## [0.26.0](https://github.com/e6qu/sockerless-cloud/compare/v0.25.0...v0.26.0) (2026-08-28)


### Features

* **gcp:** serve Cloud Build whole ([#95](https://github.com/e6qu/sockerless-cloud/issues/95)) ([f15a52f](https://github.com/e6qu/sockerless-cloud/commit/f15a52f6e548e0cb5840ca10706e32de567fa9ab))

## [0.25.0](https://github.com/e6qu/sockerless-cloud/compare/v0.24.0...v0.25.0) (2026-08-28)


### Features

* **gcp:** serve Cloud Logging and Artifact Registry whole ([#93](https://github.com/e6qu/sockerless-cloud/issues/93)) ([9403079](https://github.com/e6qu/sockerless-cloud/commit/940307950e18ee5dca5096e94f788688413743b5))

## [0.24.0](https://github.com/e6qu/sockerless-cloud/compare/v0.23.2...v0.24.0) (2026-08-28)


### Features

* **gcp:** serve the remaining tails, document by document ([#91](https://github.com/e6qu/sockerless-cloud/issues/91)) ([cfa5eb9](https://github.com/e6qu/sockerless-cloud/commit/cfa5eb9fe9ca39d5263176fd85af701d9662a097))

## [0.23.2](https://github.com/e6qu/sockerless-cloud/compare/v0.23.1...v0.23.2) (2026-08-27)


### Bug Fixes

* **sim:** a simulator does not outlive the test that started it ([#88](https://github.com/e6qu/sockerless-cloud/issues/88)) ([2cab18d](https://github.com/e6qu/sockerless-cloud/commit/2cab18dfb633403aaec185569261b7b02f79bded))

## [0.23.1](https://github.com/e6qu/sockerless-cloud/compare/v0.23.0...v0.23.1) (2026-08-27)


### Bug Fixes

* **gcp:** parse orderBy by splitting the entry, not trimming a suffix ([#87](https://github.com/e6qu/sockerless-cloud/issues/87)) ([d4a266a](https://github.com/e6qu/sockerless-cloud/commit/d4a266aa3bff55fce7661a28e6a619628ee57b69))

## [0.23.0](https://github.com/e6qu/sockerless-cloud/compare/v0.22.0...v0.23.0) (2026-08-26)


### Features

* **gcp:** close the gRPC coverage gaps — 210 of 213 methods served ([#84](https://github.com/e6qu/sockerless-cloud/issues/84)) ([81c46a7](https://github.com/e6qu/sockerless-cloud/commit/81c46a7104d71f577a53681d5bdcd33701da7a92))

## [0.22.0](https://github.com/e6qu/sockerless-cloud/compare/v0.21.0...v0.22.0) (2026-08-26)


### Features

* **gcp,azure:** serve the implementable tails, and fix what serving them exposed ([#81](https://github.com/e6qu/sockerless-cloud/issues/81)) ([93392f8](https://github.com/e6qu/sockerless-cloud/commit/93392f8a6e908f3dd301e475efceb21f2e315cda))

## [0.21.0](https://github.com/e6qu/sockerless-cloud/compare/v0.20.0...v0.21.0) (2026-08-25)


### Features

* **gcp:** Google Cloud Billing fully served — 36 of 36 ([#79](https://github.com/e6qu/sockerless-cloud/issues/79)) ([fb07e2f](https://github.com/e6qu/sockerless-cloud/commit/fb07e2f2f94d08dba0ea7d066c32c05b97425ebe))

## [0.20.0](https://github.com/e6qu/sockerless-cloud/compare/v0.19.0...v0.20.0) (2026-08-25)


### Features

* **gates:** drift locks for all three clouds, honest AWS gates, and Azure PostgreSQL stops faking ([#77](https://github.com/e6qu/sockerless-cloud/issues/77)) ([ddb0b66](https://github.com/e6qu/sockerless-cloud/commit/ddb0b6666e8b5a17ecce16e88dcfcbae1bf43a44))

## [0.19.0](https://github.com/e6qu/sockerless-cloud/compare/v0.18.1...v0.19.0) (2026-08-25)


### Features

* **aws:** the IAM derivation ratchet reaches 1,758 — copies authorize both ends, tags read the id's prefix, associations resolve through state ([#75](https://github.com/e6qu/sockerless-cloud/issues/75)) ([f867091](https://github.com/e6qu/sockerless-cloud/commit/f867091a0ee069280304828d8256af80ff3105d7))

## [0.18.1](https://github.com/e6qu/sockerless-cloud/compare/v0.18.0...v0.18.1) (2026-08-25)


### Performance Improvements

* the store-scan floor reaches zero — the last seven exemptions were conversions after all ([#73](https://github.com/e6qu/sockerless-cloud/issues/73)) ([b061fe3](https://github.com/e6qu/sockerless-cloud/commit/b061fe365faeaea1058262e549661d3609a5861a))

## [0.18.0](https://github.com/e6qu/sockerless-cloud/compare/v0.17.0...v0.18.0) (2026-08-25)


### Features

* **gcp/azure:** database data planes for Cloud SQL and Azure PostgreSQL, and backups that carry the data ([#71](https://github.com/e6qu/sockerless-cloud/issues/71)) ([5a70f9e](https://github.com/e6qu/sockerless-cloud/commit/5a70f9e1f18ac0f102f9edcc34d7752e532ae95e))

## [0.17.0](https://github.com/e6qu/sockerless-cloud/compare/v0.16.0...v0.17.0) (2026-08-24)


### Features

* **aws:** close the model drift — 42 operations AWS added since implementation ([#69](https://github.com/e6qu/sockerless-cloud/issues/69)) ([4f3895c](https://github.com/e6qu/sockerless-cloud/commit/4f3895c95b496a16dcb0d2b68823a65dcb42d3d1))

## [0.16.0](https://github.com/e6qu/sockerless-cloud/compare/v0.15.0...v0.16.0) (2026-08-24)


### Features

* **aws:** implement ECS deployment lifecycle hooks, and close the open-bug sweep ([#67](https://github.com/e6qu/sockerless-cloud/issues/67)) ([5ec2787](https://github.com/e6qu/sockerless-cloud/commit/5ec2787fd0bd01e2a6da88b023c25abd7f27c470))

## [0.15.0](https://github.com/e6qu/sockerless-cloud/compare/v0.14.0...v0.15.0) (2026-08-24)


### Features

* **aws/iam:** derive the ECS daemon and Express Mode resources ([#65](https://github.com/e6qu/sockerless-cloud/issues/65)) ([b6cae9c](https://github.com/e6qu/sockerless-cloud/commit/b6cae9cbd04497f9691bfec5e9be60392a222e57))

## [0.14.0](https://github.com/e6qu/sockerless-cloud/compare/v0.13.4...v0.14.0) (2026-08-23)


### Features

* **aws/iam:** derive the CloudWatch Logs resources that name themselves ([#63](https://github.com/e6qu/sockerless-cloud/issues/63)) ([abcb75a](https://github.com/e6qu/sockerless-cloud/commit/abcb75ac4d71dd6a8270d3d71891b103866324eb))

## [0.13.4](https://github.com/e6qu/sockerless-cloud/compare/v0.13.3...v0.13.4) (2026-08-23)


### Bug Fixes

* **aws/ecs:** publish every deployment event with the state it records ([#61](https://github.com/e6qu/sockerless-cloud/issues/61)) ([49c9c8d](https://github.com/e6qu/sockerless-cloud/commit/49c9c8d4518d854c996e8ec46cc7f68442725945))

## [0.13.3](https://github.com/e6qu/sockerless-cloud/compare/v0.13.2...v0.13.3) (2026-08-22)


### Performance Improvements

* answer a parent's children from an index, not a scan of every row ([#59](https://github.com/e6qu/sockerless-cloud/issues/59)) ([8cb2fd3](https://github.com/e6qu/sockerless-cloud/commit/8cb2fd39a535b3138a4568cb50f1b963ee3b55da))

## [0.13.2](https://github.com/e6qu/sockerless-cloud/compare/v0.13.1...v0.13.2) (2026-08-20)


### Bug Fixes

* **ci:** attribute dependency drift, so a branch answers for its own pins ([#56](https://github.com/e6qu/sockerless-cloud/issues/56)) ([110d03d](https://github.com/e6qu/sockerless-cloud/commit/110d03d2d9c872d0c7c4f503bbf030c1ff9962c9))

## [0.13.1](https://github.com/e6qu/sockerless-cloud/compare/v0.13.0...v0.13.1) (2026-08-20)


### Bug Fixes

* **azure/web:** run the azurerm backup crash to ground, and make the scan floor measure a real class ([#54](https://github.com/e6qu/sockerless-cloud/issues/54)) ([e544e33](https://github.com/e6qu/sockerless-cloud/commit/e544e33656c2ec390d46caf10aef813831a65e49))

## [0.13.0](https://github.com/e6qu/sockerless-cloud/compare/v0.12.6...v0.13.0) (2026-08-20)


### Features

* **azure/storage:** authorize every storage data-plane request, and create a network's inline subnets ([#52](https://github.com/e6qu/sockerless-cloud/issues/52)) ([b391232](https://github.com/e6qu/sockerless-cloud/commit/b391232cf1843e7860322a042e3b30cf9414f9a2))

## [0.12.6](https://github.com/e6qu/sockerless-cloud/compare/v0.12.5...v0.12.6) (2026-08-20)


### Bug Fixes

* close nine registry, Cosmos DB and workload-collection bugs, and give the freshness run somewhere to land ([#50](https://github.com/e6qu/sockerless-cloud/issues/50)) ([d651b44](https://github.com/e6qu/sockerless-cloud/commit/d651b448bb2b5936eb20fd699ed3c6ff8ea127c3))

## [0.12.5](https://github.com/e6qu/sockerless-cloud/compare/v0.12.4...v0.12.5) (2026-08-19)


### Bug Fixes

* pair every read lock with RUnlock, index the request paths that scanned a store, and hold the race count at zero ([#48](https://github.com/e6qu/sockerless-cloud/issues/48)) ([b4fd1f3](https://github.com/e6qu/sockerless-cloud/commit/b4fd1f3db35ed3df49c12202bc06cefb211a32e5))

## [0.12.4](https://github.com/e6qu/sockerless-cloud/compare/v0.12.3...v0.12.4) (2026-08-19)


### Performance Improvements

* **aws/dynamodb:** stripe the item lock, publish items copy-on-write, and end every background worker ([#45](https://github.com/e6qu/sockerless-cloud/issues/45)) ([bd6b1c5](https://github.com/e6qu/sockerless-cloud/commit/bd6b1c597e4ea89ca98976866780b5ecb9553e53))

## [0.12.3](https://github.com/e6qu/sockerless-cloud/compare/v0.12.2...v0.12.3) (2026-08-18)


### Bug Fixes

* refuse the three inputs the nightly fuzz run found the parsers accepting ([#42](https://github.com/e6qu/sockerless-cloud/issues/42)) ([1339496](https://github.com/e6qu/sockerless-cloud/commit/1339496bee98bc6e2dabda55e1f03aa45d13f2f4))

## [0.12.2](https://github.com/e6qu/sockerless-cloud/compare/v0.12.1...v0.12.2) (2026-08-18)


### Bug Fixes

* **aws/dynamodb:** copy scanned items a batch at a time, not one lock each ([#39](https://github.com/e6qu/sockerless-cloud/issues/39)) ([b3c4ae6](https://github.com/e6qu/sockerless-cloud/commit/b3c4ae6711661ed4d9c45bfc12f62146ff7949f9))


### Performance Improvements

* **aws/dynamodb:** read the partition a Query addresses, not the whole table ([#38](https://github.com/e6qu/sockerless-cloud/issues/38)) ([6ee2d0c](https://github.com/e6qu/sockerless-cloud/commit/6ee2d0ce7c6c6db99087d1652741c872ddd8cc31))

## [0.12.1](https://github.com/e6qu/sockerless-cloud/compare/v0.12.0...v0.12.1) (2026-08-17)


### Bug Fixes

* **aws/ecs:** answer a service describe from state, and serve App Service Environments and detectors ([#31](https://github.com/e6qu/sockerless-cloud/issues/31)) ([36a8114](https://github.com/e6qu/sockerless-cloud/commit/36a81145cbb0d92181cac055c42367e0c10d5094))

## [0.12.0](https://github.com/e6qu/sockerless-cloud/compare/v0.11.0...v0.12.0) (2026-08-16)


### Features

* back App Service backups with real Blob storage, and let a real engine log in to the registry ([#28](https://github.com/e6qu/sockerless-cloud/issues/28)) ([829a350](https://github.com/e6qu/sockerless-cloud/commit/829a35074e3feb7dc55ee7c0d45f2642f0f8c56e))

## [0.11.0](https://github.com/e6qu/sockerless-cloud/compare/v0.10.1...v0.11.0) (2026-08-16)


### Features

* authenticate every registry and data plane, and move twenty-nine Azure families ([#25](https://github.com/e6qu/sockerless-cloud/issues/25)) ([b52cc80](https://github.com/e6qu/sockerless-cloud/commit/b52cc80edf4791e1c80af4397a7f21a26f48142f))

## [0.10.1](https://github.com/e6qu/sockerless-cloud/compare/v0.10.0...v0.10.1) (2026-08-15)


### Bug Fixes

* **aws/ec2:** capture snapshot data behind the response, not inside it ([#23](https://github.com/e6qu/sockerless-cloud/issues/23)) ([c06550c](https://github.com/e6qu/sockerless-cloud/commit/c06550cb44f340b6e8ffd13d75ec65d7a80c483f))

## [0.10.0](https://github.com/e6qu/sockerless-cloud/compare/v0.9.2...v0.10.0) (2026-08-15)


### Features

* authenticate the registry and publish data planes, and gate engine readiness ([#21](https://github.com/e6qu/sockerless-cloud/issues/21)) ([91568d7](https://github.com/e6qu/sockerless-cloud/commit/91568d72af636790955458cf0936b5f02a65a27c))

## [0.9.2](https://github.com/e6qu/sockerless-cloud/compare/v0.9.1...v0.9.2) (2026-08-15)


### Bug Fixes

* **aws/ecs:** restore managed-EBS snapshots off the RunTask request path ([#19](https://github.com/e6qu/sockerless-cloud/issues/19)) ([3cd009f](https://github.com/e6qu/sockerless-cloud/commit/3cd009fbecb02b27ec7163b09f4436cc5fd3515a))

## [0.9.1](https://github.com/e6qu/sockerless-cloud/compare/v0.9.0...v0.9.1) (2026-08-15)


### Bug Fixes

* sweep the locally actionable bugs across both simulators ([#17](https://github.com/e6qu/sockerless-cloud/issues/17)) ([c249ede](https://github.com/e6qu/sockerless-cloud/commit/c249ede3675f99b20e5b656da70fa0d6ae326424))

## [0.9.0](https://github.com/e6qu/sockerless-cloud/compare/v0.8.0...v0.9.0) (2026-08-14)


### Features

* back Spanner admin with the real engine, and answer the resource list for real ([#15](https://github.com/e6qu/sockerless-cloud/issues/15)) ([418e0c8](https://github.com/e6qu/sockerless-cloud/commit/418e0c8482f25e9a27b27a55ac2727cda08ace9a))

## [0.8.0](https://github.com/e6qu/sockerless-cloud/compare/v0.7.0...v0.8.0) (2026-08-14)


### Features

* complete Cloud Run v1 and move Key Vaults between resource groups ([#13](https://github.com/e6qu/sockerless-cloud/issues/13)) ([1a59bde](https://github.com/e6qu/sockerless-cloud/commit/1a59bdea34079d31e6de2840899b08222d58d5e8))

## [0.7.0](https://github.com/e6qu/sockerless-cloud/compare/v0.6.0...v0.7.0) (2026-08-13)


### Features

* **azure:** complete App Service networking and dispatch resource moves per provider ([#11](https://github.com/e6qu/sockerless-cloud/issues/11)) ([5d26652](https://github.com/e6qu/sockerless-cloud/commit/5d2665236c8f067d19034839e7d573d2e6c0d787))

## [0.6.0](https://github.com/e6qu/sockerless-cloud/compare/v0.5.0...v0.6.0) (2026-08-13)


### Features

* **azure:** real certificates, DNS-truthful hostnames, real resource moves ([#9](https://github.com/e6qu/sockerless-cloud/issues/9)) ([3ef1917](https://github.com/e6qu/sockerless-cloud/commit/3ef19175b0c27103de5f4300fc3208b61eb64915))

## [0.5.0](https://github.com/e6qu/sockerless-cloud/compare/v0.4.0...v0.5.0) (2026-08-13)


### Features

* **azure:** load-bearing Functions keys, real WebJobs, real deployments ([#7](https://github.com/e6qu/sockerless-cloud/issues/7)) ([569502a](https://github.com/e6qu/sockerless-cloud/commit/569502abe39c0f73b32e3939629ba973b8bf22f6))

## [0.4.0](https://github.com/e6qu/sockerless-cloud/compare/v0.3.0...v0.4.0) (2026-08-13)


### Features

* coexist same-CIDR VPCs and complete the Static Web Apps family ([#5](https://github.com/e6qu/sockerless-cloud/issues/5)) ([665ba68](https://github.com/e6qu/sockerless-cloud/commit/665ba6820fc7162ce87a0b533964b3f2923ef2d4))

## [0.3.0](https://github.com/e6qu/sockerless-cloud/compare/v0.2.0...v0.3.0) (2026-08-13)


### Features

* widen the App Service surface, close the derivation tail, gate release titles ([#3](https://github.com/e6qu/sockerless-cloud/issues/3)) ([e24c797](https://github.com/e6qu/sockerless-cloud/commit/e24c79745d19fee6362536d8a494b501b60ac127))

## [0.2.0](https://github.com/e6qu/sockerless-cloud/compare/v0.1.0...v0.2.0) (2026-08-11)


### Features

* release with exactly one tag per version via release-please ([e94dd53](https://github.com/e6qu/sockerless-cloud/commit/e94dd538037a7358ab66ba2e534338d25086074d))
