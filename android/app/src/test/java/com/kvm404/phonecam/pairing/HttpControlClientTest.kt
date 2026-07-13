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

        val result = client(srv).pair(payload, phone, rtpPort = 40000, ssrc = 3000000000L)

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
    }

    @Test
    fun `pair surfaces error body on 401`() {
        val srv = TestHttpServer { _, _, _ ->
            401 to """{"error":"invalid pairing token"}"""
        }
        server = srv

        val result = client(srv).pair(payload, phone, rtpPort = 40000, ssrc = 42L)

        assertTrue(result is PairResult.Failure)
        assertEquals("invalid pairing token", (result as PairResult.Failure).message)
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
}
