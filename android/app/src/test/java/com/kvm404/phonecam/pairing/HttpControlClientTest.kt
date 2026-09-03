package com.kvm404.phonecam.pairing

import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import java.io.InputStream
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.nio.charset.StandardCharsets

/**
 * Exercises [HttpControlClient] against a real HTTP server bound to 127.0.0.1:0.
 *
 * A minimal ServerSocket-based responder is used instead of com.sun.net.httpserver,
 * which is not resolvable from the Android unit-test compile classpath.
 */
class HttpControlClientTest {

    /** Captures the last request body seen, per path. */
    class TestHttpServer(
        private val handler: (method: String, path: String, body: String) -> Pair<Int, String>,
    ) : AutoCloseable {
        private val serverSocket = ServerSocket(0, 0, InetAddress.getByName("127.0.0.1"))
        val port: Int get() = serverSocket.localPort
        private val thread = Thread { acceptLoop() }.apply { isDaemon = true; start() }

        private fun acceptLoop() {
            while (!serverSocket.isClosed) {
                val socket = try {
                    serverSocket.accept()
                } catch (e: Exception) {
                    return
                }
                try {
                    socket.use { handle(it) }
                } catch (e: Exception) {
                    // Ignore per-connection failures during teardown.
                }
            }
        }

        private fun handle(socket: Socket) {
            val input = socket.getInputStream()
            val headerText = readHeaders(input)
            if (headerText.isEmpty()) return
            val lines = headerText.split("\r\n")
            val requestLine = lines[0].split(" ")
            val method = requestLine.getOrElse(0) { "GET" }
            val path = requestLine.getOrElse(1) { "/" }
            val contentLength = lines.drop(1)
                .firstOrNull { it.startsWith("Content-Length:", ignoreCase = true) }
                ?.substringAfter(":")?.trim()?.toIntOrNull() ?: 0
            val body = readBody(input, contentLength)

            val (status, responseBody) = handler(method, path, body)
            val respBytes = responseBody.toByteArray(StandardCharsets.UTF_8)
            val header = buildString {
                append("HTTP/1.1 ").append(status).append(' ').append(reason(status)).append("\r\n")
                append("Content-Type: application/json\r\n")
                append("Content-Length: ").append(respBytes.size).append("\r\n")
                append("Connection: close\r\n\r\n")
            }
            socket.getOutputStream().apply {
                write(header.toByteArray(StandardCharsets.US_ASCII))
                write(respBytes)
                flush()
            }
        }

        private fun readHeaders(input: InputStream): String {
            val sb = StringBuilder()
            var state = 0 // counts consecutive \r\n\r\n progress
            while (true) {
                val b = input.read()
                if (b < 0) break
                val c = b.toChar()
                sb.append(c)
                state = when {
                    c == '\r' && (state == 0 || state == 2) -> state + 1
                    c == '\n' && state == 1 -> 2
                    c == '\n' && state == 3 -> 4
                    else -> 0
                }
                if (state == 4) break
            }
            return sb.toString()
        }

        private fun readBody(input: InputStream, length: Int): String {
            if (length <= 0) return ""
            val bytes = ByteArray(length)
            var read = 0
            while (read < length) {
                val r = input.read(bytes, read, length - read)
                if (r < 0) break
                read += r
            }
            return String(bytes, 0, read, StandardCharsets.UTF_8)
        }

        private fun reason(status: Int): String = when (status) {
            200 -> "OK"
            202 -> "Accepted"
            401 -> "Unauthorized"
            410 -> "Gone"
            else -> "Status"
        }

        override fun close() {
            serverSocket.close()
            thread.interrupt()
        }
    }

    private var server: TestHttpServer? = null

    private val payload = PairingPayload(
        version = 1,
        name = "laptop",
        control = "http://unused:1",
        rtp = "10.0.0.2:5004",
        rtpHost = "10.0.0.2",
        rtpPort = 5004,
        session = "sess-123",
        token = "tok-abc",
        expires = "2999-01-01T00:00:00Z",
        transport = "rtp-h264",
        video = VideoProfile(1280, 720, 30),
    )

    private val phone = PhoneIdentity(id = "phone-1", name = "Pixel")

    @Before
    fun setUp() {
        // server started per-test with a specific handler
    }

    @After
    fun tearDown() {
        server?.close()
    }

    private fun client(server: TestHttpServer) =
        HttpControlClient(baseUrlOverride = "http://127.0.0.1:${server.port}")

    @Test
    fun `pair returns Accepted on 202`() {
        var captured: String? = null
        val srv = TestHttpServer { _, path, body ->
            if (path == "/pair") {
                captured = body
                202 to """{"ok":true,"approved":false,"session":"sess-123"}"""
            } else {
                404 to """{"error":"not found"}"""
            }
        }
        server = srv

        val result = client(srv).pair(
            payload,
            phone,
            rtpPort = 40000,
            ssrc = 3000000000L,
            video = VideoProfile(1280, 720, 30),
        )

        assertTrue(result is PairResult.Accepted)
        result as PairResult.Accepted
        assertEquals(false, result.approved)
        assertEquals("sess-123", result.session)

        val body = JSONObject(requireNotNull(captured))
        assertEquals("sess-123", body.getString("session"))
        assertEquals("tok-abc", body.getString("token"))
        assertEquals(40000, body.getInt("rtp_port"))
        assertEquals(3000000000L, body.getLong("ssrc"))
        assertEquals("phone-1", body.getJSONObject("phone").getString("id"))
        assertEquals("Pixel", body.getJSONObject("phone").getString("name"))
        val video = body.getJSONObject("video")
        assertEquals(1280, video.getInt("width"))
        assertEquals(720, video.getInt("height"))
        assertEquals(30, video.getInt("fps"))
        assertTrue(!body.has("camera"))
    }

    @Test
    fun `pair body carries optional camera`() {
        var captured: String? = null
        val srv = TestHttpServer { _, path, body ->
            if (path == "/pair") {
                captured = body
                202 to """{"ok":true,"approved":false,"session":"sess-123"}"""
            } else {
                404 to """{"error":"not found"}"""
            }
        }
        server = srv

        client(srv).pair(
            payload,
            phone,
            rtpPort = 40000,
            ssrc = 42L,
            video = VideoProfile(1280, 720, 30),
            camera = "front",
        )

        assertEquals("front", JSONObject(requireNotNull(captured)).getString("camera"))
    }

    @Test
    fun `pair body carries rotated (swapped) video dims`() {
        var captured: String? = null
        val srv = TestHttpServer { _, path, body ->
            if (path == "/pair") {
                captured = body
                202 to """{"ok":true,"approved":false,"session":"sess-123"}"""
            } else {
                404 to """{"error":"not found"}"""
            }
        }
        server = srv

        // Portrait streaming: encoder emits 720x1280 even though the payload advertised 1280x720.
        client(srv).pair(
            payload,
            phone,
            rtpPort = 40000,
            ssrc = 42L,
            video = VideoProfile(720, 1280, 30),
        )

        val video = JSONObject(requireNotNull(captured)).getJSONObject("video")
        assertEquals(720, video.getInt("width"))
        assertEquals(1280, video.getInt("height"))
        assertEquals(30, video.getInt("fps"))
    }

    @Test
    fun `pair surfaces error body on 401`() {
        val srv = TestHttpServer { _, _, _ ->
            401 to """{"error":"invalid pairing token"}"""
        }
        server = srv

        val result = client(srv).pair(
            payload,
            phone,
            rtpPort = 40000,
            ssrc = 42L,
            video = VideoProfile(1280, 720, 30),
        )

        assertTrue(result is PairResult.Failure)
        assertEquals("invalid pairing token", (result as PairResult.Failure).message)
    }

    @Test
    fun `status parses last_rtp_ms and request_keyframe`() {
        val srv = TestHttpServer { _, _, _ ->
            200 to """{"ok":true,"approved":true,"session":"sess-123","last_rtp_ms":42,"request_keyframe":true}"""
        }
        server = srv

        val result = client(srv).status(payload)
        assertTrue(result is StatusResult.Ok)
        result as StatusResult.Ok
        assertEquals(true, result.approved)
        assertEquals(42L, result.lastRtpMs)
        assertEquals(true, result.requestKeyframe)
    }

    @Test
    fun `status reflects approval flip`() {
        val approved = java.util.concurrent.atomic.AtomicBoolean(false)
        val srv = TestHttpServer { _, _, _ ->
            200 to """{"ok":true,"approved":${approved.get()},"session":"sess-123"}"""
        }
        server = srv

        val first = client(srv).status(payload)
        assertTrue(first is StatusResult.Ok)
        assertEquals(false, (first as StatusResult.Ok).approved)

        approved.set(true)
        val second = client(srv).status(payload)
        assertTrue(second is StatusResult.Ok)
        assertEquals(true, (second as StatusResult.Ok).approved)
    }

    @Test
    fun `pair treats HTTP 200 as failure`() {
        val srv = TestHttpServer { _, path, _ ->
            if (path == "/pair") {
                200 to """{"ok":true,"approved":true,"session":"sess-123","resume_token":"secret"}"""
            } else {
                404 to """{"error":"not found"}"""
            }
        }
        server = srv

        val result = client(srv).pair(
            payload,
            phone,
            rtpPort = 40000,
            ssrc = 42L,
            video = VideoProfile(1280, 720, 30),
        )

        assertTrue(result is PairResult.Failure)
    }

    @Test
    fun `pair 202 extra keys are ignored and secrets parsed`() {
        val srv = TestHttpServer { _, path, _ ->
            if (path == "/pair") {
                202 to """{"ok":true,"approved":true,"session":"sess-123","resume_token":"rt-1","pairing_secret":"ps-1","extra":true}"""
            } else {
                404 to """{"error":"not found"}"""
            }
        }
        server = srv

        val result = client(srv).pair(
            payload,
            phone,
            rtpPort = 40000,
            ssrc = 42L,
            video = VideoProfile(1280, 720, 30),
        )

        assertTrue(result is PairResult.Accepted)
        result as PairResult.Accepted
        assertEquals(true, result.approved)
        assertEquals("rt-1", result.resumeToken)
        assertEquals("ps-1", result.pairingSecret)
    }

    @Test
    fun `reconnect returns Ok on 200 and ignores extra keys`() {
        var captured: String? = null
        val srv = TestHttpServer { _, path, body ->
            if (path == "/reconnect") {
                captured = body
                200 to """{"ok":true,"approved":true,"session":"sess-123","resume_token":"rt-live","control":"http://10.0.0.2:47470","rtp":"10.0.0.2:47471","video":{"width":640,"height":360,"fps":30},"extra":1}"""
            } else {
                404 to """{"error":"not found"}"""
            }
        }
        server = srv

        val result = client(srv).reconnect(
            ReconnectRequest(
                payload = payload,
                phone = phone,
                rtpPort = 40100,
                ssrc = 99L,
                video = VideoProfile(640, 360, 30),
                resumeToken = "rt-live",
                camera = "back",
            ),
        )

        assertTrue(result is ReconnectResult.Ok)
        result as ReconnectResult.Ok
        assertEquals(true, result.approved)
        assertEquals("sess-123", result.session)
        assertEquals("rt-live", result.resumeToken)
        assertEquals("http://10.0.0.2:47470", result.control)
        assertEquals("10.0.0.2:47471", result.rtp)
        assertEquals(VideoProfile(640, 360, 30), result.video)

        val body = JSONObject(requireNotNull(captured))
        assertEquals("phone-1", body.getJSONObject("phone").getString("id"))
        assertEquals(40100, body.getInt("rtp_port"))
        assertEquals(99L, body.getLong("ssrc"))
        assertEquals("rt-live", body.getString("resume_token"))
        assertEquals("back", body.getString("camera"))
    }

    @Test
    fun `reconnect treats non-200 as failure`() {
        val srv = TestHttpServer { _, _, _ ->
            202 to """{"ok":true,"approved":true,"session":"sess-123","resume_token":"rt"}"""
        }
        server = srv

        val result = client(srv).reconnect(
            ReconnectRequest(
                payload = payload,
                phone = phone,
                rtpPort = 40100,
                ssrc = 99L,
                video = VideoProfile(640, 360, 30),
                resumeToken = "rt",
            ),
        )

        assertTrue(result is ReconnectResult.Failure)
    }

    @Test
    fun `leave posts session and token`() {
        var captured: String? = null
        var method: String? = null
        var path: String? = null
        val srv = TestHttpServer { m, p, body ->
            method = m
            path = p
            captured = body
            200 to """{"ok":true}"""
        }
        server = srv

        assertTrue(client(srv).leave(payload))
        assertEquals("POST", method)
        assertEquals("/leave", path)
        val body = JSONObject(requireNotNull(captured))
        assertEquals(payload.session, body.getString("session"))
        assertEquals(payload.token, body.getString("token"))
    }
}
