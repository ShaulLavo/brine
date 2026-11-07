#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <sched.h>
#include <stdio.h>
#include <sys/syscall.h>
#include <unistd.h>

static void *hold(void *arg) {
    if (unshare(CLONE_FILES) != 0) {
        perror("unshare");
        return (void *)1;
    }
    int fd = open((char *)arg, O_RDONLY | O_CLOEXEC);
    if (fd < 0) {
        perror("open");
        return (void *)1;
    }
    printf("%ld\n", syscall(SYS_gettid));
    fflush(stdout);
    char byte;
    if (read(STDIN_FILENO, &byte, 1) != 1 || byte != 'c') {
        close(fd);
        return (void *)1;
    }
    close(fd);
    printf("closed\n");
    fflush(stdout);
    while (read(STDIN_FILENO, &byte, 1) > 0) {}
    return NULL;
}

int main(int argc, char **argv) {
    if (argc != 2) return 2;
    pthread_t thread;
    if (pthread_create(&thread, NULL, hold, argv[1]) != 0) return 3;
    void *result;
    if (pthread_join(thread, &result) != 0) return 4;
    return result != NULL;
}
