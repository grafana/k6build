import { check } from "k6"
import http from "k6/http";
import { openKv } from "k6/x/kv";
import { AWSConfig, S3Client } from 'https://jslib.k6.io/aws/0.12.3/s3.js';

import { randomFromArray, randomFromDict } from "./lib/utils.js"

// kv tore used to coordinate builders and user
const kv = openKv();

const buildSrvURL = __ENV.K6_BUILD_SERVICE_URL || 'http://localhost:8000';
const buildSrvToken = __ENV.K6_BUILD_SERVICE_AUTH || __ENV.K6_CLOUD_TOKEN
const buildSrvEndpoint = `${buildSrvURL}/build`

// set request headers. Add authorization token if defined
const buildSrvHeaders = {"Content-Type": "application/json"}
if (buildSrvToken) {
        buildSrvHeaders["Authorization"] = `Bearer ${buildSrvToken}`
}

// supported k6 versions
const k6Versions = ["v0.55.0", "v0.56.0", "v0.57.0"]

// supported extensions
const extensions = {
        "k6/x/faker": ["v0.4.0"],
        "k6/x/ssh": ["v0.1.1", "v0.1.0"],
}

// creates a random build request piking a combination of a k6 version and an extension
function randomBuildRequest() {
        const k6Version = randomFromArray(k6Versions)
        const ext = randomFromDict(extensions)
        const extVersion = randomFromArray(extensions[ext])

        return {
                "k6": k6Version,
                "dependencies": [
                        { "name": ext, "constraints": "=" + extVersion }
                ],
                "platform": "linux/amd64"
        }
}

// deletes all artifacts from the store bucket
async function cleanupAWS() {
        if (!__ENV.CLEANUP_AWS || __ENV.CLEANUP_AWS == "") {
                return
        }

        const bucket = __ENV.K6_BUILD_SERVICE_BUCKET
        if ( bucket == "") {
                throw new Error("aborting AWS cleanup K6_BUILD_SERVICE_BUCKET not specified ")
        }
              
        const awsConfig = new AWSConfig({
                region: __ENV.AWS_REGION,
                accessKeyId: __ENV.AWS_ACCESS_KEY_ID,
                secretAccessKey: __ENV.AWS_SECRET_ACCESS_KEY,
                sessionToken: __ENV.AWS_SESSION_TOKEN
        });

        const s3 = new S3Client(awsConfig);

        const artifacts = await s3.listObjects(bucket);

        for (const artifact of artifacts) {
                console.log(`deleting object ${artifact.key}`)
                await s3.deleteObject(bucket, artifact.key)
        }
}

export async function setup() {
        await kv.clear();

        await cleanupAWS()
}

// make a build request
export async function build() {
        const request = JSON.stringify(randomBuildRequest())
        const resp = http.post(
                buildSrvEndpoint,
                request,
                {
                        headers: buildSrvHeaders
                }
        );

        const ok = check(resp, {
                'is status 200': (r) => r.status === 200,
                'get success': (r) => !r.json().error,
        });

        if (ok) {
                kv.set("build:" + resp.json().artifact.id, request)
        }
}

// make a request for an already-build artifact
export async function use() {
        const builds = await kv.list({ prefix: "build:" })
        if (builds.length == 0) {
                return
        }

        const request = randomFromArray(builds.map( e => e.value))

        const resp = http.post(
                buildSrvEndpoint,
                request,
                {
                        headers: buildSrvHeaders
                }
        );

        check(resp, {
                'is status 200': (r) => r.status === 200,
                'get success': (r) => !r.json().error,
        });
}

export let options = {
        scenarios: {
                builder: {
                        executor: "constant-arrival-rate",
                        rate: __ENV.BUILD_RATE || 1,
                        timeUnit: "1m",
                        duration: __ENV.TEST_DURATION || "10m" ,
                        preAllocatedVUs: 1,
                        maxVUs: 3,
                        exec: "build",
                },
                user: {
                        executor: "constant-arrival-rate",
                        rate: __ENV.USER_RATE || 10,
                        timeUnit: "1m",
                        startTime: "1m",
                        duration: __ENV.TEST_DURATION || "10m" ,
                        preAllocatedVUs: 5,
                        maxVUs: 10,
                        exec: "use",
                }
        },
}
