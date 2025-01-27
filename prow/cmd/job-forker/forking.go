// Copyright (c) 2022 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"k8s.io/test-infra/prow/config"
	"k8s.io/test-infra/prow/github"
	"sigs.k8s.io/yaml"

	ghi "github.com/gardener/ci-infra/prow/pkg/githubinteractor"
)

func createTargetFileName(repository, branch string) string {
	repoString := strings.ReplaceAll(repository, "/", "-")
	branchString := strings.ReplaceAll(branch, ".", "-")
	return fmt.Sprintf("%s-%s.yaml", repoString, branchString)
}

func generatePresubmits(j config.JobConfig, repository string, branch github.Branch) []config.Presubmit {
	newPresubmits := []config.Presubmit{}
	for _, presubmit := range j.PresubmitsStatic[repository] {
		if presubmit.Annotations[ForkAnnotation] != "true" {
			continue
		}
		delete(presubmit.Annotations, ForkAnnotation)
		presubmit.Annotations[ForkedAnnotation] = "true"
		presubmit.Name = fmt.Sprintf("%s-%s", presubmit.Name, strings.ReplaceAll(branch.Name, ".", "-"))
		presubmit.Branches = []string{branch.Name}
		presubmit.SkipBranches = nil

		newPresubmits = append(newPresubmits, presubmit)
	}
	return newPresubmits
}

func generatePostsubmits(j config.JobConfig, repository string, branch github.Branch) []config.Postsubmit {
	newPostsubmits := []config.Postsubmit{}
	for _, postsubmit := range j.PostsubmitsStatic[repository] {
		if postsubmit.Annotations[ForkAnnotation] != "true" {
			continue
		}
		delete(postsubmit.Annotations, ForkAnnotation)
		postsubmit.Annotations[ForkedAnnotation] = "true"
		postsubmit.Name = fmt.Sprintf("%s-%s", postsubmit.Name, strings.ReplaceAll(branch.Name, ".", "-"))
		postsubmit.Branches = []string{branch.Name}
		postsubmit.SkipBranches = nil

		newPostsubmits = append(newPostsubmits, postsubmit)
	}
	return newPostsubmits
}

func generatePeriodics(j config.JobConfig, repository string, branch github.Branch) []config.Periodic {
	newPeriodics := []config.Periodic{}
	for _, periodic := range j.Periodics {
		if periodic.Annotations[ForkAnnotation] != "true" {
			continue
		}
		delete(periodic.Annotations, ForkAnnotation)
		periodic.Annotations[ForkedAnnotation] = "true"
		periodic.Name = fmt.Sprintf("%s-%s", periodic.Name, strings.ReplaceAll(branch.Name, ".", "-"))

		isRelatedToRepo := false
		for i, ref := range periodic.ExtraRefs {
			if ref.OrgRepoString() != repository {
				continue
			}
			isRelatedToRepo = true
			periodic.ExtraRefs[i].BaseRef = branch.Name
		}

		if !isRelatedToRepo {
			continue
		}

		newPeriodics = append(newPeriodics, periodic)

	}

	return newPeriodics
}

func forkJobs(repository string, releaseBranch github.Branch, jobDirectoryPath, outputDirectory string, filenames []string) (bool, error) {
	log.Printf("Start forking for branch %s of repository %s", releaseBranch.Name, repository)

	presubmits := []config.Presubmit{}
	postsubmits := []config.Postsubmit{}
	periodics := []config.Periodic{}

	targetDir := path.Join(jobDirectoryPath, outputDirectory)
	newFileName := createTargetFileName(repository, releaseBranch.Name)
	targetFile := path.Join(targetDir, filepath.Base(newFileName))

	if _, err := os.Stat(targetFile); err == nil {
		log.Printf("File %s is already existing, skip job forking for branch %s of repository %s", targetFile, releaseBranch.Name, repository)
		return false, nil
	}

	for _, file := range filenames {
		j, err := config.ReadJobConfig(file)
		if err != nil {
			return false, fmt.Errorf("couldn't read jobConfig: %w", err)
		}
		presubmits = append(presubmits, generatePresubmits(j, repository, releaseBranch)...)
		postsubmits = append(postsubmits, generatePostsubmits(j, repository, releaseBranch)...)
		periodics = append(periodics, generatePeriodics(j, repository, releaseBranch)...)
	}

	payload := config.JobConfig{}

	if len(presubmits) != 0 {
		payload.PresubmitsStatic = map[string][]config.Presubmit{repository: presubmits}
	}

	if len(postsubmits) != 0 {
		payload.PostsubmitsStatic = map[string][]config.Postsubmit{repository: postsubmits}
	}

	if len(periodics) != 0 {
		payload.Periodics = periodics
	}

	if len(presubmits) == 0 && len(postsubmits) == 0 && len(periodics) == 0 {
		log.Printf("No prow jobs found, which should be forked for branch %s of %s\n", releaseBranch.Name, repository)
		return false, nil
	}

	err := os.MkdirAll(targetDir, os.ModePerm)
	if err != nil {
		return false, fmt.Errorf("couldn't create output directory: %w", err)
	}

	data, err := yaml.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("couldn't marshal prow jobs: %w", err)
	}

	newf, err := os.Create(targetFile)
	if err != nil {
		return false, fmt.Errorf("couldn't create output file: %w", err)
	}
	defer newf.Close()

	if _, err = newf.Write(data); err != nil {
		return false, fmt.Errorf("couldn't write to outputFile: %w", err)
	}
	log.Printf("%v has forked %v Presubmits, %v Postsubmits, %v Periodics for release branch %s into %s\n",
		repository,
		len(presubmits),
		len(postsubmits),
		len(periodics),
		releaseBranch.Name,
		targetFile,
	)
	return true, nil
}

func removeOrphanedJobs(repository string, releaseBranches []github.Branch, jobDirectoryPath, outputDirectory string) (bool, error) {
	var changes bool

	log.Printf("Start searching for orphaned jobs")

	repoString := strings.ReplaceAll(repository, "/", "-")
	forkedDir := path.Join(jobDirectoryPath, outputDirectory)
	forkedFiles, err := ghi.GetFileNames(forkedDir, []string{}, false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("couldn't read from forked files Directory: %w", err)
	}
	log.Printf("Existing files in output-directory %s: %v\n", outputDirectory, forkedFiles)

	for _, forkedFile := range forkedFiles {
		if filepath.Ext(forkedFile) != ".yaml" && filepath.Ext(forkedFile) != ".yml" {
			log.Printf("%s is not a yaml file\n", forkedFile)
			continue
		}
		if !strings.HasPrefix(filepath.Base(forkedFile), repoString) {
			log.Printf("%s didn't match %s. It does not belong to repo %s\n", forkedFile, repository, repository)
			continue
		}
		log.Printf("%s appears to be created by job-forker\n", forkedFile)
		// branched File belongs to repo
		matches := false
		for _, releaseBranch := range releaseBranches {
			if filepath.Base(forkedFile) == createTargetFileName(repository, releaseBranch.Name) {
				// branched File has corresponding branch
				matches = true
				break
			}
		}

		if !matches {
			// File is deprecated and has no corresponding branch to it anymore
			log.Printf("Deleting %v, because its branch in repository %s does not exist anymore\n", forkedFile, repository)
			if err = os.Remove(forkedFile); err != nil {
				return false, err
			}
			changes = true
		}
	}
	if !changes {
		log.Printf("No orphaned jobs found")
	}
	return changes, nil
}

func getReposFromJobFiles(files []string) ([]string, error) {
	repoMap := make(map[string]bool)
	for _, file := range files {
		j, err := config.ReadJobConfig(file)
		if err != nil {
			return nil, fmt.Errorf("couldn't read jobConfig: %w", err)
		}
		for repo := range j.PresubmitsStatic {
			repoMap[repo] = true
		}
		for repo := range j.PostsubmitsStatic {
			repoMap[repo] = true
		}
		for _, periodic := range j.Periodics {
			for _, ref := range periodic.ExtraRefs {
				repo := ref.OrgRepoString()
				repoMap[repo] = true
			}
		}
	}
	repos := make([]string, len(repoMap))
	var i uint
	for key := range repoMap {
		repos[i] = key
		i++
	}
	return repos, nil
}

func generateForkedConfigurations(upstreamRepo *ghi.Repository, o options) (bool, error) {
	var changes bool
	jobDirectoryPath := path.Join(upstreamRepo.RepoClient.Directory(), o.jobDirectory)
	fileNames, err := ghi.GetFileNames(jobDirectoryPath, []string{o.outputDirectory}, o.recursive)
	log.Printf("Files in prow job path: %v\n", fileNames)
	if err != nil {
		return false, err
	}

	jobRepos, err := getReposFromJobFiles(fileNames)
	if err != nil {
		return false, err
	}
	for _, jobRepo := range jobRepos {
		rep, err := ghi.NewRepository(jobRepo, upstreamRepo.Gh)
		if err != nil {
			return false, err
		}

		releaseBranches, err := rep.GetMatchingBranches(o.releaseBranchPattern)
		if err != nil {
			return false, err
		}

		log.Printf("There are %v release branches for repo %v\n", len(releaseBranches), rep.FullRepoName)
		// Check if there is a release branch without a corresponding forked config
		for _, releaseBranch := range releaseBranches {
			result, err := forkJobs(rep.FullRepoName, releaseBranch, jobDirectoryPath, o.outputDirectory, fileNames)
			if err != nil {
				return false, err
			}
			changes = changes || result
		}
		result, err := removeOrphanedJobs(rep.FullRepoName, releaseBranches, jobDirectoryPath, o.outputDirectory)
		if err != nil {
			return false, err
		}
		changes = changes || result
	}
	return changes, nil
}
