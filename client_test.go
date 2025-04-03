/*
 * Copyright (C) 2019, 2020, 2021, 2022. Genome Research Ltd. All rights
 * reserved.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License,
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 * @file client_test.go
 * @author Keith James <kdj@sanger.ac.uk>
 */

package extendo_test

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	ex "github.com/wtsi-npg/extendo/v2"
)

var _ = Describe("Find the baton-do executable", func() {
	var savedPath string

	When("the executable is on the PATH", func() {
		It("should be found", func() {
			path, err := ex.FindBaton()
			Expect(err).NotTo(HaveOccurred())
			Expect(path).ToNot(BeEmpty())
			Expect(filepath.Base(path)).To(Equal("baton-do"))
		})

		It("should report a version", func() {
			version, err := ex.BatonVersion()
			Expect(err).NotTo(HaveOccurred())
			Expect(version).To(MatchRegexp(`^\d+.\d+.\d+.*`))
		})
	})

	When("the executable is not on the PATH", func() {
		BeforeEach(func() {
			savedPath = os.Getenv("PATH")
			err := os.Unsetenv("PATH")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			err := os.Setenv("PATH", savedPath)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should return an error", func() {
			_, err := ex.FindBaton()
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError("baton-do not present in PATH ''"))
		})
	})
})

var _ = Describe("Start and stop the Item client", func() {
	var (
		client *ex.Client
		err    error
	)

	BeforeEach(func() {
		client, err = ex.FindAndStart(batonArgs...)
	})

	Describe("Stop and start", func() {
		When("the client is not running", func() {
			It("should start without error", func() {
				Expect(err).NotTo(HaveOccurred())
			})

			It("should be running", func() {
				Expect(client.IsRunning()).To(BeTrue())
			})

			When("Start is attempted", func() {
				It("should return an error", func() {
					_, err := client.Start()
					Expect(err).To(HaveOccurred())
					Expect(err).To(MatchError("client is already running"))
				})
			})
		})

		When("the client is running", func() {
			BeforeEach(func() {
				err = client.Stop()
			})

			It("should stop without error", func() {
				Expect(err).NotTo(HaveOccurred())
			})

			It("should not be running", func() {
				Expect(client.IsRunning()).To(BeFalse())
			})
		})

	})
})

var _ = Describe("List an iRODS path", func() {
	var (
		client *ex.Client
		err    error

		rootColl, workColl string
		testColl, testObj  ex.RodsItem

		testChecksum = "1181c1834012245d785120e3505ed169"

		getRodsPaths itemPathTransform
	)

	BeforeEach(func() {
		client, err = ex.FindAndStart(batonArgs...)
		Expect(err).NotTo(HaveOccurred())

		rootColl = "/testZone/home/irods"
		workColl = tmpRodsPath(rootColl, "ExtendoList")

		getRodsPaths = makeRodsItemTransform(workColl)

		err = putTestData("testdata/", workColl)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		err = removeTmpCollection(workColl)
		Expect(err).NotTo(HaveOccurred())

		client.StopIgnoreError()
	})

	When("the path does not exist", func() {
		It("should return an iRODS -310000 error", func() {
			path := filepath.Join(rootColl, "does_not_exist")
			item := ex.RodsItem{IPath: path}

			_, err := client.ListItem(ex.Args{}, item)
			Expect(err).To(HaveOccurred())

			code, e := ex.RodsErrorCode(err)
			Expect(e).NotTo(HaveOccurred())

			Expect(code).To(Equal(ex.RodsUserFileDoesNotExist))
		})
	})

	When("the path does not exist and contains printf placeholders", func() {
		It("should return an iRODS -310000 error", func() {
			path := filepath.Join(rootColl, "%s")
			item := ex.RodsItem{IPath: path}

			_, err := client.ListItem(ex.Args{}, item)
			Expect(err).To(HaveOccurred())

			code, e := ex.RodsErrorCode(err)
			Expect(e).NotTo(HaveOccurred())

			Expect(code).To(Equal(ex.RodsUserFileDoesNotExist))
		})
	})

	When("the item is a collection", func() {
		BeforeEach(func() {
			testColl = ex.RodsItem{IPath: filepath.Join(workColl, "testdata")}
		})

		Context("multiple items are requested", func() {
			It("should return a RodsItem with that path", func() {
				items, err := client.List(ex.Args{}, testColl)
				Expect(err).NotTo(HaveOccurred())

				Expect(items).To(HaveLen(1))
				Expect(items[0].IPath).To(Equal(testColl.IPath))
			})

			When("contents are requested", func() {
				It("should return contents", func() {
					items, err := client.List(ex.Args{Contents: true}, testColl)
					Expect(err).NotTo(HaveOccurred())
					Expect(items).To(HaveLen(1))

					expected := []string{"testdata/1", "testdata/testdir"}
					contents := items[0].IContents
					Expect(contents).To(WithTransform(getRodsPaths, ConsistOf(expected)))
				})
			})

			When("metadata are requested", func() {
				BeforeEach(func() {
					testColl.IAVUs = []ex.AVU{{Attr: "test_attr_x", Value: "y"}}
					_, err = client.MetaAdd(ex.Args{}, testColl)
					Expect(err).NotTo(HaveOccurred())
				})

				It("should have metadata", func() {
					items, err := client.List(ex.Args{AVU: true}, testColl)
					Expect(err).NotTo(HaveOccurred())

					Expect(items).To(HaveLen(1))
					metadata := items[0].IAVUs
					Expect(metadata).To(Equal([]ex.AVU{{Attr: "test_attr_x", Value: "y"}}))
				})
			})

			When("ACLs are requested", func() {
				It("should have ACLs if requested", func() {
					items, err := client.List(ex.Args{ACL: true}, testColl)
					Expect(err).NotTo(HaveOccurred())

					Expect(items).To(HaveLen(1))
					acls := items[0].IACLs
					Expect(acls).To(Equal([]ex.ACL{{Owner: "irods",
						Level: "own", Zone: "testZone"}}))
				})
			})

			When("contents are recursed", func() {
				It("should return a recursive slice of contents", func() {
					items, err := client.List(ex.Args{Recurse: true}, testColl)
					Expect(err).NotTo(HaveOccurred())

					expectedItems := []string{
						"testdata",
						"testdata/1",
						"testdata/1/reads",
						"testdata/1/reads/fast5",
						"testdata/1/reads/fastq",
						"testdata/testdir",
						"testdata/1/reads/fast5/reads1.fast5",
						"testdata/1/reads/fast5/reads1.fast5.md5",
						"testdata/1/reads/fast5/reads2.fast5",
						"testdata/1/reads/fast5/reads3.fast5",
						"testdata/1/reads/fastq/reads1.fastq",
						"testdata/1/reads/fastq/reads1.fastq.md5",
						"testdata/1/reads/fastq/reads2.fastq",
						"testdata/1/reads/fastq/reads3.fastq",
						"testdata/testdir/.gitignore",
					}

					Expect(items).To(WithTransform(getRodsPaths, ConsistOf(expectedItems)))
				})
			})

			When("getting metadata for many files at once", func() {
				It("retrieves the metadata for the correct file", func() {
					files := [...]string{
						"testdata/1/reads/fast5/reads1.fast5",
						"testdata/1/reads/fast5/reads1.fast5.md5",
						"testdata/1/reads/fast5/reads2.fast5",
						"testdata/1/reads/fast5/reads3.fast5",
						"testdata/1/reads/fastq/reads1.fastq",
						"testdata/1/reads/fastq/reads1.fastq.md5",
						"testdata/1/reads/fastq/reads2.fastq",
						"testdata/1/reads/fastq/reads3.fastq",
						"testdata/testdir/.gitignore",
					}

					var (
						wg        sync.WaitGroup
						retrieved [len(files)]ex.RodsItem
						errors    [len(files)]error
					)

					for _, file := range files {
						_, err := client.MetaAdd(ex.Args{}, ex.RodsItem{
							IPath: filepath.Join(workColl, filepath.Dir(file)),
							IName: filepath.Base(file),
							IAVUs: []ex.AVU{
								{Attr: "path", Value: file},
							},
						})

						Expect(err).NotTo(HaveOccurred())
					}

					wg.Add(len(files))

					for n, file := range files {
						go func(n int, file string) {
							defer wg.Done()

							retrieved[n], errors[n] = client.ListItem(ex.Args{AVU: true}, ex.RodsItem{
								IPath: filepath.Join(workColl, filepath.Dir(file)),
								IName: filepath.Base(file),
							})
						}(n, file)
					}

					wg.Wait()

					for _, err := range errors {
						Expect(err).NotTo(HaveOccurred())
					}

					for n, file := range files {
						Expect(retrieved[n].IAVUs[0].Value).To(Equal(file))
					}
				})
			})
		})

		Context("a single item is requested", func() {
			It("should return a RodsItem with that path", func() {
				item, err := client.ListItem(ex.Args{}, testColl)
				Expect(err).NotTo(HaveOccurred())
				Expect(item.IPath).To(Equal(testColl.IPath))
			})

			When("a recursive list are requested", func() {
				It("should return an error", func() {
					_, err := client.ListItem(ex.Args{Recurse: true}, testColl)
					Expect(err).To(HaveOccurred())

					Expect(err).To(MatchError("invalid argument: " +
						"Recurse=true"))
				})
			})
		})
	})

	When("the item is a data object", func() {
		BeforeEach(func() {
			testObj = ex.RodsItem{
				IPath: filepath.Join(workColl, "testdata/1/reads/fast5"),
				IName: "reads1.fast5"}
		})

		Context("multiple items are requested", func() {
			It("should return a RodsItem with that path and name", func() {
				items, err := client.List(ex.Args{}, testObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(items[0].IName).To(Equal(testObj.IName))
				Expect(items[0].IPath).To(Equal(testObj.IPath))
			})

			It("should have a checksum if requested", func() {
				items, err := client.List(ex.Args{Checksum: true}, testObj)
				Expect(err).NotTo(HaveOccurred())

				Expect(items).To(HaveLen(1))
				Expect(items[0].IChecksum).To(Equal(testChecksum))
			})

			It("should have a size if requested", func() {
				items, err := client.List(ex.Args{Size: true}, testObj)
				Expect(err).NotTo(HaveOccurred())

				var expected uint64 = 4
				Expect(items).To(HaveLen(1))
				Expect(items[0].ISize).To(Equal(expected))
			})

			It("should have replicate information if requested", func() {
				items, err := client.List(ex.Args{Replicate: true}, testObj)
				Expect(err).NotTo(HaveOccurred())

				Expect(items).To(HaveLen(1))
				reps := items[0].IReplicates

				// NB: When baton is built against the iRODS 4.2.7 client
				// libraries, it ignores the irods_default_resource in
				// irods_environment.json. This causes files to hit a
				// non-replication resource, even when one is requested,
				// leading to a single replicates.
				//
				// When baton is built against the iRODS 4.2.10 client
				// libraries, it respects the irods_default_resource in
				// irods_environment.json.

				// The replication resource assigns replicate number randomly.
				expectedRepsX := []ex.Replicate{
					{
						Resource: "unixfs1",
						Location: "localhost",
						Checksum: testChecksum,
						Number:   0,
						Valid:    true,
					},
					{
						Resource: "unixfs2",
						Location: "localhost",
						Checksum: testChecksum,
						Number:   1,
						Valid:    true},
				}
				expectedRepsY := []ex.Replicate{
					{
						Resource: "unixfs1",
						Location: "localhost",
						Checksum: testChecksum,
						Number:   1,
						Valid:    true,
					},
					{
						Resource: "unixfs2",
						Location: "localhost",
						Checksum: testChecksum,
						Number:   0,
						Valid:    true,
					},
				}

				Expect(reps).To(Or(
					ConsistOf(expectedRepsX),
					ConsistOf(expectedRepsY)))
			})

			It("should have timestamp information if requested", func() {
				items, err := client.List(ex.Args{Timestamp: true}, testObj)
				Expect(err).NotTo(HaveOccurred())

				Expect(items).To(HaveLen(1))
				stamps := items[0].ITimestamps
				Expect(stamps).To(Or(HaveLen(4)))

				// TODO: We could write a custom matcher to allow us to use
				// ConsistsOf. However, that needs a lot more code than the
				// simple repetition below.
				Expect(stamps[0].Created).
					To(BeTemporally("~", time.Now(), time.Minute))
				Expect(stamps[0].Modified).To(BeZero())
				Expect(stamps[0].Replicates).To(Equal(0))

				Expect(stamps[1].Created).To(BeZero())
				Expect(stamps[1].Modified).
					To(BeTemporally("~", time.Now(), time.Minute))
				Expect(stamps[1].Replicates).To(Equal(0))

				Expect(stamps[2].Created).
					To(BeTemporally("~", time.Now(), time.Minute))
				Expect(stamps[2].Modified).To(BeZero())
				Expect(stamps[2].Replicates).To(Equal(1))

				Expect(stamps[3].Created).To(BeZero())
				Expect(stamps[3].Modified).
					To(BeTemporally("~", time.Now(), time.Minute))
				Expect(stamps[3].Replicates).To(Equal(1))
			})

			When("metadata are requested", func() {
				BeforeEach(func() {
					testObj.IAVUs = []ex.AVU{
						{Attr: "test_attr_a", Value: "1"},
						{Attr: "test_attr_b", Value: "2"},
						{Attr: "test_attr_c", Value: "3"}}
					_, err = client.MetaAdd(ex.Args{}, testObj)
					Expect(err).NotTo(HaveOccurred())
				})

				It("should have metadata", func() {
					items, err := client.List(ex.Args{AVU: true}, testObj)
					Expect(err).NotTo(HaveOccurred())

					Expect(items).To(HaveLen(1))
					metadata := items[0].IAVUs

					Expect(metadata).To(Equal([]ex.AVU{
						{Attr: "test_attr_a", Value: "1"},
						{Attr: "test_attr_b", Value: "2"},
						{Attr: "test_attr_c", Value: "3"}}))
				})
			})

			It("should have ACLs if requested", func() {
				items, err := client.List(ex.Args{ACL: true}, testObj)
				Expect(err).NotTo(HaveOccurred())

				Expect(items).To(HaveLen(1))
				acls := items[0].IACLs
				Expect(acls).To(Equal([]ex.ACL{{Owner: "irods",
					Level: "own", Zone: "testZone"}}))
			})
		})

		Context("a single item is requested", func() {
			It("should return a RodsItem with that path and name", func() {
				item, err := client.ListItem(ex.Args{}, testObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(item.IName).To(Equal(testObj.IName))
				Expect(item.IPath).To(Equal(testObj.IPath))
			})
		})
	})
})

var _ = Describe("Put a file into iRODS", func() {
	var (
		client *ex.Client
		err    error

		rootColl, workColl      string
		existingObject, testObj ex.RodsItem

		testChecksum = "1181c1834012245d785120e3505ed169"
	)

	BeforeEach(func() {
		client, err = ex.FindAndStart(batonArgs...)
		Expect(err).NotTo(HaveOccurred())

		rootColl = "/testZone/home/irods"
		workColl = tmpRodsPath(rootColl, "ExtendoPut")

		err = putTestData("testdata/", workColl)
		Expect(err).NotTo(HaveOccurred())

		objDir, err := filepath.Abs("testdata/1/reads/fast5")
		Expect(err).NotTo(HaveOccurred())

		coll := filepath.Join(workColl, "testdata/1/reads/fast5")
		existingObject = ex.RodsItem{IDirectory: objDir,
			IFile: "reads1.fast5",
			IPath: coll, IName: "reads1.fast5"}

		testObj = ex.RodsItem{IDirectory: objDir,
			IFile: "reads1.fast5",
			IPath: workColl, IName: "reads99.fast5"}
	})

	AfterEach(func() {
		err = removeTmpCollection(workColl)
		Expect(err).NotTo(HaveOccurred())

		client.StopIgnoreError()
	})

	When("a new data object is put into iRODS", func() {
		It("should be present in iRODS afterwards", func() {
			items, err := client.Put(ex.Args{}, testObj)
			Expect(err).NotTo(HaveOccurred())

			item, err := client.ListItem(ex.Args{}, items[0])
			Expect(err).NotTo(HaveOccurred())

			Expect(item.IPath).To(Equal(testObj.IPath))
			Expect(item.IName).To(Equal(testObj.IName))
		})

		It("should not have a checksum by default", func() {
			items, err := client.Put(ex.Args{}, testObj)
			Expect(err).NotTo(HaveOccurred())
			Expect(items).To(HaveLen(1))

			checksum, err := client.ListChecksum(items[0])
			Expect(err).NotTo(HaveOccurred())
			Expect(checksum).To(Equal(""))
		})

		When("a checksum is requested", func() {
			It("should have a checksum", func() {
				_, err := client.Put(ex.Args{Checksum: true}, testObj)
				Expect(err).NotTo(HaveOccurred())

				checksum, err := client.ListChecksum(testObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(checksum).To(Equal(testChecksum))
			})
		})
	})

	When("overwriting a data object", func() {
		It("should not return an error", func() {
			items, err := client.Put(ex.Args{}, existingObject)
			Expect(err).NotTo(HaveOccurred())
			Expect(items[0].IPath).To(Equal(existingObject.IPath))
			Expect(items[0].IName).To(Equal(existingObject.IName))
		})
	})
})

var _ = Describe("Put a directory into iRODS", func() {
	var (
		client *ex.Client
		err    error

		rootColl, workColl string

		getRodsPaths  itemPathTransform
		getLocalPaths localPathTransform
	)

	BeforeEach(func() {
		client, err = ex.FindAndStart(batonArgs...)
		Expect(err).NotTo(HaveOccurred())

		rootColl = "/testZone/home/irods"
		workColl = tmpRodsPath(rootColl, "ExtendoPut")

		getRodsPaths = makeRodsItemTransform(workColl)
		getLocalPaths = makeLocalFileTransform("testdata")
	})

	When("a local directory is put into iRODS, without recursion", func() {
		It("should fail to create that collection", func() {
			testItem :=
				ex.RodsItem{IDirectory: "testdata", IPath: workColl}
			Expect(testItem.IsLocalDir()).To(BeTrue())
			Expect(testItem.IsCollection()).To(BeTrue())
			Expect(testItem.IsLocalFile()).To(BeFalse())
			Expect(testItem.IsDataObject()).To(BeFalse())

			// iRODS considers a non-recursive put to be a transfer of
			// a file to a data object. A local file does not exist
			// (just a local directory), so iRODS' client code returns
			// an error.
			_, err := client.Put(ex.Args{Recurse: false}, testItem)
			Expect(err).To(HaveOccurred())

			code, e := ex.RodsErrorCode(err)
			Expect(e).NotTo(HaveOccurred())

			Expect(code).To(Equal(ex.RodsUserFileDoesNotExist))
		})
	})

	When("a local directory is put into iRODS, with recursion", func() {
		AfterEach(func() {
			err = removeTmpCollection(workColl)
			Expect(err).NotTo(HaveOccurred())

			client.StopIgnoreError()
		})

		It("should create a tree, including data objects", func() {
			testItem := ex.RodsItem{IDirectory: "testdata", IPath: workColl}
			Expect(testItem.IsLocalDir()).To(BeTrue())
			Expect(testItem.IsCollection()).To(BeTrue())
			Expect(testItem.IsLocalFile()).To(BeFalse())
			Expect(testItem.IsDataObject()).To(BeFalse())

			items, err := client.Put(ex.Args{Recurse: true}, testItem)
			Expect(err).NotTo(HaveOccurred())

			expectedItems := []string{
				"testdata/1/reads/fast5/reads1.fast5",
				"testdata/1/reads/fast5/reads1.fast5.md5",
				"testdata/1/reads/fast5/reads2.fast5",
				"testdata/1/reads/fast5/reads3.fast5",
				"testdata/1/reads/fastq/reads1.fastq",
				"testdata/1/reads/fastq/reads1.fastq.md5",
				"testdata/1/reads/fastq/reads2.fastq",
				"testdata/1/reads/fastq/reads3.fastq",
				"testdata/testdir/.gitignore",
			}
			Expect(items).To(WithTransform(getRodsPaths,
				ConsistOf(expectedItems)))

			expectedFiles := []string{
				"1/reads/fast5/reads1.fast5",
				"1/reads/fast5/reads1.fast5.md5",
				"1/reads/fast5/reads2.fast5",
				"1/reads/fast5/reads3.fast5",
				"1/reads/fastq/reads1.fastq",
				"1/reads/fastq/reads1.fastq.md5",
				"1/reads/fastq/reads2.fastq",
				"1/reads/fastq/reads3.fastq",
				"testdir/.gitignore",
			}
			Expect(items).To(WithTransform(getLocalPaths,
				ConsistOf(expectedFiles)))
		})
	})
})

var _ = Describe("Remove a data object from iRODS", func() {
	var (
		client *ex.Client
		err    error

		rootColl, workColl string

		testObj      ex.RodsItem
		testChecksum = "1181c1834012245d785120e3505ed169"
	)

	BeforeEach(func() {
		client, err = ex.FindAndStart(batonArgs...)
		Expect(err).NotTo(HaveOccurred())

		rootColl = "/testZone/home/irods"
		workColl = tmpRodsPath(rootColl, "ExtendoList")

		err = putTestData("testdata/", workColl)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		err = removeTmpCollection(workColl)
		Expect(err).NotTo(HaveOccurred())

		client.StopIgnoreError()
	})

	When("a data object is removed", func() {
		BeforeEach(func() {
			testObj = ex.RodsItem{
				IPath: filepath.Join(workColl, "testdata/1/reads/fast5"),
				IName: "reads1.fast5"}
		})

		It("should be absent afterwards", func() {
			item, err := client.ListItem(ex.Args{Checksum: true}, testObj)
			Expect(err).NotTo(HaveOccurred())
			Expect(item.RodsPath()).To(Equal(testObj.RodsPath()))
			Expect(item.IChecksum).To(Equal(testChecksum))

			_, err = client.RemObj(ex.Args{}, item)
			Expect(err).NotTo(HaveOccurred())

			_, err = client.ListItem(ex.Args{Checksum: true}, testObj)
			Expect(err).To(HaveOccurred())

			code, e := ex.RodsErrorCode(err)
			Expect(e).NotTo(HaveOccurred())

			Expect(code).To(Equal(ex.RodsUserFileDoesNotExist))
		})
	})
})

var _ = Describe("Remove an iRODS collection", func() {
	var (
		client *ex.Client
		err    error

		rootColl, workColl string

		testColl ex.RodsItem
	)

	BeforeEach(func() {
		client, err = ex.FindAndStart(batonArgs...)
		Expect(err).NotTo(HaveOccurred())

		rootColl = "/testZone/home/irods"
		workColl = tmpRodsPath(rootColl, "ExtendoList")

		err = putTestData("testdata/", workColl)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		err = removeTmpCollection(workColl)
		Expect(err).NotTo(HaveOccurred())

		client.StopIgnoreError()
	})

	When("the collection is empty", func() {
		BeforeEach(func() {
			testColl = ex.RodsItem{IPath: filepath.Join(workColl, "emptyColl")}
			_, err := client.MkDir(ex.Args{}, testColl)
			Expect(err).NotTo(HaveOccurred())
		})

		When("the collection is removed", func() {
			It("should be absent afterwards", func() {
				item, err := client.ListItem(ex.Args{}, testColl)
				Expect(err).NotTo(HaveOccurred())
				Expect(item.RodsPath()).To(Equal(testColl.RodsPath()))

				_, err = client.RemDir(ex.Args{}, testColl)
				Expect(err).NotTo(HaveOccurred())

				item, err = client.ListItem(ex.Args{}, testColl)
				Expect(err).To(HaveOccurred())

				code, e := ex.RodsErrorCode(err)
				Expect(e).NotTo(HaveOccurred())

				Expect(code).To(Equal(ex.RodsUserFileDoesNotExist))
			})
		})
	})

	When("the collection has contents", func() {
		BeforeEach(func() {
			testColl = ex.RodsItem{IPath: filepath.Join(workColl, "fullColl")}
			_, err := client.MkDir(ex.Args{}, testColl)
			Expect(err).NotTo(HaveOccurred())

			objDir, err := filepath.Abs("testdata/1/reads/fast5")
			Expect(err).NotTo(HaveOccurred())

			testObj := ex.RodsItem{IDirectory: objDir, IFile: "reads1.fast5",
				IPath: testColl.RodsPath(), IName: "reads1.fast5"}

			_, err = client.Put(ex.Args{}, testObj)
			Expect(err).NotTo(HaveOccurred())
		})

		When("the collection is removed, without recursion", func() {
			It("should return an iRODS -821000 error", func() {
				_, err := client.RemDir(ex.Args{Force: false}, testColl)
				Expect(err).To(HaveOccurred())

				code, e := ex.RodsErrorCode(err)
				Expect(e).NotTo(HaveOccurred())

				Expect(code).To(Equal(ex.RodsCatCollectionNotEmpty))
			})
		})

		When("the collection is removed, with recursion", func() {
			It("should be absent afterwards", func() {
				item, err := client.ListItem(ex.Args{}, testColl)
				Expect(err).NotTo(HaveOccurred())
				Expect(item.RodsPath()).To(Equal(testColl.RodsPath()))

				_, err = client.RemDir(ex.Args{Recurse: true}, testColl)
				Expect(err).NotTo(HaveOccurred())

				item, err = client.ListItem(ex.Args{}, testColl)
				Expect(err).To(HaveOccurred())

				code, e := ex.RodsErrorCode(err)
				Expect(e).NotTo(HaveOccurred())

				Expect(code).To(Equal(ex.RodsUserFileDoesNotExist))
			})
		})
	})
})

var _ = Describe("Calculate a data object checksum", func() {
	var (
		client *ex.Client
		err    error

		rootColl, workColl string

		testObj      ex.RodsItem
		testChecksum = "1181c1834012245d785120e3505ed169"
	)

	BeforeEach(func() {
		client, err = ex.FindAndStart(batonArgs...)
		Expect(err).NotTo(HaveOccurred())

		rootColl = "/testZone/home/irods"
		workColl = tmpRodsPath(rootColl, "ExtendoPut")

		err = putTestData("testdata/", workColl)
		Expect(err).NotTo(HaveOccurred())

		objDir, err := filepath.Abs("testdata/1/reads/fast5")
		Expect(err).NotTo(HaveOccurred())

		testObj = ex.RodsItem{IDirectory: objDir,
			IFile: "reads1.fast5",
			IPath: workColl, IName: "reads99.fast5"}
	})

	AfterEach(func() {
		err = removeTmpCollection(workColl)
		Expect(err).NotTo(HaveOccurred())

		client.StopIgnoreError()
	})

	When("a data object has no checksum", func() {
		It("should have a checksum afterwards", func() {
			_, err := client.Put(ex.Args{}, testObj)
			Expect(err).NotTo(HaveOccurred())

			checksum, err := client.ListChecksum(testObj)
			Expect(err).NotTo(HaveOccurred())
			Expect(checksum).To(Equal(""))

			_, err = client.Checksum(ex.Args{}, testObj)
			Expect(err).NotTo(HaveOccurred())

			checksum, err = client.ListChecksum(testObj)
			Expect(err).NotTo(HaveOccurred())
			Expect(checksum).To(Equal(testChecksum))
		})
	})
})

var _ = Describe("Add access permissions", func() {
	var (
		client *ex.Client
		err    error

		rootColl, workColl string
		testColl, testObj  ex.RodsItem

		publicRead = ex.ACL{Owner: "public", Level: "read", Zone: "testZone"}
	)

	BeforeEach(func() {
		client, err = ex.FindAndStart(batonArgs...)
		Expect(err).NotTo(HaveOccurred())

		rootColl = "/testZone/home/irods"
		workColl = tmpRodsPath(rootColl, "ExtendoChmod")

		err = putTestData("testdata/", workColl)
		Expect(err).NotTo(HaveOccurred())

		testColl = ex.RodsItem{
			IPath: filepath.Join(workColl, "testdata")}
		testObj = ex.RodsItem{
			IPath: filepath.Join(workColl, "testdata/1/reads/fast5"),
			IName: "reads1.fast5"}
	})

	AfterEach(func() {
		err = removeTmpCollection(workColl)
		Expect(err).NotTo(HaveOccurred())

		client.StopIgnoreError()
	})

	Context("setting permissions on collections", func() {
		When("adding access for a group", func() {
			It("should add an ACL", func() {
				item, err := client.ListItem(ex.Args{ACL: true}, testColl)
				Expect(err).NotTo(HaveOccurred())

				Expect(item.IACLs).NotTo(ContainElement(publicRead))

				testColl.IACLs = []ex.ACL{publicRead}
				_, err = client.Chmod(ex.Args{}, testColl)
				Expect(err).NotTo(HaveOccurred())

				item, err = client.ListItem(ex.Args{ACL: true}, testColl)
				Expect(err).NotTo(HaveOccurred())

				Expect(item.IACLs).To(ContainElement(publicRead))
			})
		})
	})

	Context("setting permissions on data objects", func() {
		When("adding access for a group", func() {
			It("should add an ACL", func() {
				item, err := client.ListItem(ex.Args{ACL: true}, testObj)
				Expect(err).NotTo(HaveOccurred())

				Expect(item.IACLs).NotTo(ContainElement(publicRead))

				testObj.IACLs = []ex.ACL{publicRead}
				_, err = client.Chmod(ex.Args{}, testObj)
				Expect(err).NotTo(HaveOccurred())

				item, err = client.ListItem(ex.Args{ACL: true}, testObj)
				Expect(err).NotTo(HaveOccurred())

				Expect(item.IACLs).To(ContainElement(publicRead))
			})
		})
	})
})

var _ = Describe("Remove access permissions", func() {
	var (
		client *ex.Client
		err    error

		rootColl, workColl string
		testColl, testObj  ex.RodsItem

		publicRead = ex.ACL{Owner: "public", Level: "read", Zone: "testZone"}
		publicNull = ex.ACL{Owner: "public", Level: "null", Zone: "testZone"}
	)

	BeforeEach(func() {
		client, err = ex.FindAndStart(batonArgs...)
		Expect(err).NotTo(HaveOccurred())

		rootColl = "/testZone/home/irods"
		workColl = tmpRodsPath(rootColl, "ExtendoChmod")

		err = putTestData("testdata/", workColl)
		Expect(err).NotTo(HaveOccurred())

		testColl = ex.RodsItem{
			IPath: filepath.Join(workColl, "testdata")}
		testColl.IACLs = []ex.ACL{publicRead}
		_, err = client.Chmod(ex.Args{}, testColl)
		Expect(err).NotTo(HaveOccurred())

		testObj = ex.RodsItem{
			IPath: filepath.Join(workColl, "testdata/1/reads/fast5"),
			IName: "reads1.fast5"}
		testObj.IACLs = []ex.ACL{publicRead}
		_, err = client.Chmod(ex.Args{}, testObj)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		err = removeTmpCollection(workColl)
		Expect(err).NotTo(HaveOccurred())

		client.StopIgnoreError()
	})

	Context("setting permissions on collections", func() {
		When("removing access for a group", func() {
			It("should remove an ACL", func() {
				item, err := client.ListItem(ex.Args{ACL: true}, testColl)
				Expect(err).NotTo(HaveOccurred())

				Expect(item.IACLs).To(ContainElement(publicRead))

				testColl.IACLs = []ex.ACL{publicNull}
				_, err = client.Chmod(ex.Args{}, testColl)
				Expect(err).NotTo(HaveOccurred())

				item, err = client.ListItem(ex.Args{ACL: true}, testColl)
				Expect(err).NotTo(HaveOccurred())

				Expect(item.IACLs).NotTo(ContainElement(publicRead))
			})
		})
	})

	Context("setting permissions on data objects", func() {
		When("removing access for a group", func() {
			It("should remove an ACL", func() {
				item, err := client.ListItem(ex.Args{ACL: true}, testObj)
				Expect(err).NotTo(HaveOccurred())

				Expect(item.IACLs).To(ContainElement(publicRead))

				testObj.IACLs = []ex.ACL{publicNull}
				_, err = client.Chmod(ex.Args{}, testObj)
				Expect(err).NotTo(HaveOccurred())

				item, err = client.ListItem(ex.Args{ACL: true}, testObj)
				Expect(err).NotTo(HaveOccurred())

				Expect(item.IACLs).NotTo(ContainElement(publicRead))
			})
		})
	})
})

var _ = Describe("Metadata query", func() {
	var (
		client *ex.Client
		err    error

		rootColl, workColl string

		getRodsPaths itemPathTransform
	)

	BeforeEach(func() {
		client, err = ex.FindAndStart(batonArgs...)
		Expect(err).NotTo(HaveOccurred())

		rootColl = "/testZone/home/irods"
		workColl = tmpRodsPath(rootColl, "ExtendoMeta")
		getRodsPaths = makeRodsItemTransform(workColl)

		err = putTestData("testdata/", workColl)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		err = removeTmpCollection(workColl)
		Expect(err).NotTo(HaveOccurred())

		client.StopIgnoreError()
	})

	When("querying without specifying object or collection", func() {
		It("should return an error", func() {
			var emptyArgs = ex.Args{}
			_, err := client.MetaQuery(emptyArgs,
				ex.RodsItem{IAVUs: []ex.AVU{{Attr: "test_attr_a", Value: "1"}}})
			Expect(err).To(HaveOccurred())

			pattern := `metaquery arguments must specify.*neither were specified`
			Expect(err).To(MatchError(MatchRegexp(pattern)))
		})
	})

	Context("querying collections", func() {
		BeforeEach(func() {
			testColl := ex.RodsItem{
				IPath: filepath.Join(workColl, "testdata")}
			testColl.IAVUs = []ex.AVU{{Attr: "test_attr_x", Value: "y"}}
			_, err = client.MetaAdd(ex.Args{}, testColl)
			Expect(err).NotTo(HaveOccurred())
		})

		When("a query is run", func() {
			It("should return collections", func() {
				items, err := client.MetaQuery(ex.Args{Collection: true},
					ex.RodsItem{IAVUs: []ex.AVU{{Attr: "test_attr_x", Value: "y"}}})
				Expect(err).NotTo(HaveOccurred())

				Expect(items).To(HaveLen(1))
				Expect(items[0].IPath).To(Equal(filepath.Join(workColl, "testdata")))
				Expect(items[0].IsCollection()).To(BeTrue())
			})
		})
	})

	Context("querying data objects", func() {
		BeforeEach(func() {
			testColl := ex.RodsItem{
				IPath: filepath.Join(workColl, "testdata")}
			items, err := client.List(ex.Args{Recurse: true}, testColl)
			Expect(err).NotTo(HaveOccurred())

			for _, item := range items {
				item.IAVUs = []ex.AVU{{Attr: "test_attr_a", Value: "1"}}
				_, err = client.MetaAdd(ex.Args{}, item)
			}
			Expect(err).NotTo(HaveOccurred())
		})

		When("a query is run", func() {
			It("should return data objects", func() {
				items, err := client.MetaQuery(ex.Args{Object: true},
					ex.RodsItem{IAVUs: []ex.AVU{{Attr: "test_attr_a", Value: "1"}}})
				Expect(err).NotTo(HaveOccurred())

				expectedItems := []string{
					"testdata/1/reads/fast5/reads1.fast5",
					"testdata/1/reads/fast5/reads1.fast5.md5",
					"testdata/1/reads/fast5/reads2.fast5",
					"testdata/1/reads/fast5/reads3.fast5",
					"testdata/1/reads/fastq/reads1.fastq",
					"testdata/1/reads/fastq/reads1.fastq.md5",
					"testdata/1/reads/fastq/reads2.fastq",
					"testdata/1/reads/fastq/reads3.fastq",
					"testdata/testdir/.gitignore",
				}

				Expect(items).To(WithTransform(getRodsPaths,
					ConsistOf(expectedItems)))
			})
		})
	})
})

var _ = Describe("Add metadata", func() {
	var (
		client *ex.Client
		err    error

		rootColl, workColl string
		testColl, testObj  ex.RodsItem

		newAVU = ex.AVU{Attr: "abcdefgh", Value: "1234567890"}
	)

	BeforeEach(func() {
		client, err = ex.FindAndStart(batonArgs...)
		Expect(err).NotTo(HaveOccurred())

		rootColl = "/testZone/home/irods"
		workColl = tmpRodsPath(rootColl, "ExtendoMeta")

		err = putTestData("testdata/", workColl)
		Expect(err).NotTo(HaveOccurred())

		testColl = ex.RodsItem{
			IPath: filepath.Join(workColl, "testdata")}
		testObj = ex.RodsItem{
			IPath: filepath.Join(workColl, "testdata/1/reads/fast5"),
			IName: "reads1.fast5"}
	})

	AfterEach(func() {
		err = removeTmpCollection(workColl)
		Expect(err).NotTo(HaveOccurred())

		client.StopIgnoreError()
	})

	Context("adding metadata to collections", func() {
		When("adding an AVU", func() {
			It("should be added", func() {
				item, err := client.ListItem(ex.Args{AVU: true}, testColl)
				Expect(err).NotTo(HaveOccurred())

				Expect(item.IAVUs).NotTo(ContainElement(newAVU))

				testColl.IAVUs = []ex.AVU{newAVU}
				_, err = client.MetaAdd(ex.Args{}, testColl)
				Expect(err).NotTo(HaveOccurred())

				item, err = client.ListItem(ex.Args{AVU: true}, testColl)
				Expect(err).NotTo(HaveOccurred())

				Expect(item.IAVUs).To(ContainElement(newAVU))
			})
		})
	})

	Context("adding metadata to data objects", func() {
		When("adding an AVU", func() {
			It("should be added", func() {
				item, err := client.ListItem(ex.Args{AVU: true}, testObj)
				Expect(err).NotTo(HaveOccurred())

				Expect(item.IAVUs).NotTo(ContainElement(newAVU))

				testObj.IAVUs = []ex.AVU{newAVU}
				_, err = client.MetaAdd(ex.Args{}, testObj)
				Expect(err).NotTo(HaveOccurred())

				item, err = client.ListItem(ex.Args{AVU: true}, testObj)
				Expect(err).NotTo(HaveOccurred())

				Expect(item.IAVUs).To(ContainElement(newAVU))
			})
		})
	})
})
